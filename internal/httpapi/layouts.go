package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
	"uuid"

	"github.com/gin-gonic/gin"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// layoutHandler holds the handlers of the layout routes, like hallHandler for halls. Layouts are
// nested the same way, one level down: creating and listing them is /halls/{id}/layouts, and a
// layout that exists is /layouts/{id} ("shallow nesting", see hallHandler).
type layoutHandler struct {
	service *event.LayoutService
	logger  *slog.Logger
}

// layoutsPath is where single layouts live; the Location of a new layout points into it.
const layoutsPath = "/api/v1/admin/layouts"

// layoutID reads the :id part of a /layouts/:id path. An id that isn't a UUID can't name a
// layout, so it is a 404, as for halls; layoutID then writes that response and returns false.
func layoutID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		writeProblem(c, http.StatusNotFound, event.ErrLayoutNotFound.Error())
		return uuid.UUID{}, false
	}
	return id, true
}

// writeLayout sends one layout with its version as the ETag, which a later change sends back in
// If-Match.
func writeLayout(c *gin.Context, status int, layout event.Layout) {
	setETag(c, layout.Version)
	c.JSON(status, toLayoutResponse(layout))
}

// create handles POST /api/v1/admin/halls/:id/layouts. It answers 201 Created with the new layout
// (a draft), its ETag, and its URL in the Location header.
func (h layoutHandler) create(c *gin.Context) {
	hallID, ok := hallID(c)
	if !ok {
		return
	}
	var req layoutRequest
	if !decodeJSON(c, &req, jsonContentType) {
		return
	}

	layout, err := h.service.Create(c.Request.Context(), hallID, req.toInput())
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	c.Header("Location", layoutsPath+"/"+layout.ID.String())
	writeLayout(c, http.StatusCreated, layout)
}

// list handles GET /api/v1/admin/halls/:id/layouts?status=active: every layout of the hall with
// this status (default active, as for halls; ?status=draft lists the ones being set up), sorted
// by name, as {"layouts": [...]}. No paging, since a hall has only a few layouts.
func (h layoutHandler) list(c *gin.Context) {
	hallID, ok := hallID(c)
	if !ok {
		return
	}
	status := event.LayoutActive
	if s := c.Query("status"); s != "" {
		status = event.LayoutStatus(s) // the service checks it is one of the statuses
	}

	layouts, err := h.service.List(c.Request.Context(), hallID, status)
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	resp := layoutListResponse{Layouts: make([]layoutResponse, len(layouts))} // [] when empty, not null
	for i, layout := range layouts {
		resp.Layouts[i] = toLayoutResponse(layout)
	}
	c.JSON(http.StatusOK, resp)
}

// get handles GET /api/v1/admin/layouts/:id, archived layouts too.
func (h layoutHandler) get(c *gin.Context) {
	id, ok := layoutID(c)
	if !ok {
		return
	}
	layout, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	writeLayout(c, http.StatusOK, layout)
}

// update handles PATCH /api/v1/admin/layouts/:id, a JSON Merge Patch of the version named in
// If-Match, like PATCH /halls/:id: the body is checked against the create body's shape first,
// then decoded into layoutPatch, which records which fields were sent.
func (h layoutHandler) update(c *gin.Context) {
	id, ok := layoutID(c)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(c)
	if !ok {
		return
	}
	body, ok := readBody(c, mergePatchContentType, jsonContentType)
	if !ok {
		return
	}
	if !decodeStrict(c, body, &layoutRequest{}) {
		return
	}
	var patch layoutPatch
	if err := json.Unmarshal(body, &patch); err != nil {
		// Can't happen for a body that passed the check above, so it would be our bug (500).
		writeServiceError(c, h.logger, fmt.Errorf("decode layout patch: %w", err))
		return
	}

	layout, err := h.service.Update(c.Request.Context(), id, version, func(in *event.LayoutInput) error {
		patch.apply(in)
		return nil
	})
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	writeLayout(c, http.StatusOK, layout)
}

// activate handles POST /api/v1/admin/layouts/:id/activate of the version named in If-Match: a
// draft with at least one section becomes active, so events can be created in it.
func (h layoutHandler) activate(c *gin.Context) {
	h.changeStatus(c, h.service.Activate)
}

// deactivate handles POST /api/v1/admin/layouts/:id/deactivate of the version named in If-Match:
// an active layout goes back to a draft, so its sections and seats can be changed.
func (h layoutHandler) deactivate(c *gin.Context) {
	h.changeStatus(c, h.service.Deactivate)
}

// archive handles POST /api/v1/admin/layouts/:id/archive of the version named in If-Match.
func (h layoutHandler) archive(c *gin.Context) {
	h.changeStatus(c, h.service.Archive)
}

// restore handles POST /api/v1/admin/layouts/:id/restore: an archived layout comes back as a
// draft.
func (h layoutHandler) restore(c *gin.Context) {
	h.changeStatus(c, h.service.Restore)
}

// changeStatus is the shared body of activate, deactivate, archive and restore, like hallHandler.changeStatus.
func (h layoutHandler) changeStatus(c *gin.Context, op func(context.Context, uuid.UUID, int) (event.Layout, error)) {
	id, ok := layoutID(c)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(c)
	if !ok {
		return
	}
	layout, err := op(c.Request.Context(), id, version)
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	writeLayout(c, http.StatusOK, layout)
}

// delete handles DELETE /api/v1/admin/layouts/:id of the version named in If-Match, and answers
// 204 No Content. Its sections and their seats are deleted with it.
func (h layoutHandler) delete(c *gin.Context) {
	id, ok := layoutID(c)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), id, version); err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// layoutRequest is the JSON body of POST /halls/{id}/layouts; PATCH /layouts/{id} checks its body
// against it too. There is no status: a layout is created as a draft and changes status only
// through its own requests (activate, deactivate, archive, restore), and an unknown field is a
// 400.
type layoutRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

func (r layoutRequest) toInput() event.LayoutInput {
	return event.LayoutInput{Name: r.Name, Description: r.Description}
}

// layoutPatch is the JSON body of PATCH /layouts/{id}: any subset of a layout's fields, with the
// three states of patchField (left out, null, a value).
type layoutPatch struct {
	Name        patchField[string] `json:"name"`
	Description patchField[string] `json:"description"`
}

// apply changes in, the layout's current values, by the patch. null clears the description; a
// null name becomes "", which the service reports as "is required".
func (p layoutPatch) apply(in *event.LayoutInput) {
	setRequired(p.Name, &in.Name)
	setOptional(p.Description, &in.Description)
}

// layoutResponse is the JSON form of a layout. Optional values are null rather than left out, as
// in hallResponse.
type layoutResponse struct {
	ID          uuid.UUID          `json:"id"`
	HallID      uuid.UUID          `json:"hall_id"`
	Name        string             `json:"name"`
	Description *string            `json:"description"`
	Status      event.LayoutStatus `json:"status"`
	Version     int                `json:"version"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

func toLayoutResponse(l event.Layout) layoutResponse {
	return layoutResponse{
		ID:          l.ID,
		HallID:      l.HallID,
		Name:        l.Name,
		Description: l.Description,
		Status:      l.Status,
		Version:     l.Version,
		CreatedAt:   l.CreatedAt.UTC(),
		UpdatedAt:   l.UpdatedAt.UTC(),
	}
}

// layoutListResponse is the JSON form of a hall's layouts: {"layouts": [...]}.
type layoutListResponse struct {
	Layouts []layoutResponse `json:"layouts"`
}
