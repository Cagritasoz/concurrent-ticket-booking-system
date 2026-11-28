package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
	"uuid"

	"github.com/gin-gonic/gin"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// sectionHandler holds the handlers of the section routes, like layoutHandler for layouts.
// Sections are nested the same way, one level down: creating and listing them is
// /layouts/{id}/sections, and a section that exists is /sections/{id}. They have no status, so
// there is no archive or restore: a section changes or is deleted while its layout is a draft.
type sectionHandler struct {
	service *event.SectionService
	logger  *slog.Logger
}

// sectionsPath is where single sections live; the Location of a new section points into it.
const sectionsPath = "/api/v1/admin/sections"

// sectionID reads the :id part of a /sections/:id path. An id that isn't a UUID can't name a
// section, so it is a 404, as for layouts; sectionID then writes that response and returns false.
func sectionID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		writeProblem(c, http.StatusNotFound, event.ErrSectionNotFound.Error())
		return uuid.UUID{}, false
	}
	return id, true
}

// writeSection sends one section with its version as the ETag, which a later change sends back
// in If-Match.
func writeSection(c *gin.Context, status int, section event.Section) {
	setETag(c, section.Version)
	c.JSON(status, toSectionResponse(section))
}

// create handles POST /api/v1/admin/layouts/:id/sections. It answers 201 Created with the new
// section, its ETag, and its URL in the Location header.
func (h sectionHandler) create(c *gin.Context) {
	layoutID, ok := layoutID(c) // a malformed id is a 404 for the layout, as on every /layouts/:id route
	if !ok {
		return
	}
	var req sectionRequest
	if !decodeJSON(c, &req, jsonContentType) {
		return
	}

	section, err := h.service.Create(c.Request.Context(), layoutID, req.toInput())
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	c.Header("Location", sectionsPath+"/"+section.ID.String())
	writeSection(c, http.StatusCreated, section)
}

// list handles GET /api/v1/admin/layouts/:id/sections: every section of the layout in the
// layout's order (position, then name), as {"sections": [...]}. No paging and no status filter:
// sections have no status, and a layout has at most a few hundred.
func (h sectionHandler) list(c *gin.Context) {
	layoutID, ok := layoutID(c)
	if !ok {
		return
	}
	sections, err := h.service.List(c.Request.Context(), layoutID)
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	resp := sectionListResponse{Sections: make([]sectionResponse, len(sections))} // [] when empty, not null
	for i, section := range sections {
		resp.Sections[i] = toSectionResponse(section)
	}
	c.JSON(http.StatusOK, resp)
}

// get handles GET /api/v1/admin/sections/:id.
func (h sectionHandler) get(c *gin.Context) {
	id, ok := sectionID(c)
	if !ok {
		return
	}
	section, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	writeSection(c, http.StatusOK, section)
}

// update handles PATCH /api/v1/admin/sections/:id, a JSON Merge Patch of the version named in
// If-Match, like PATCH /layouts/:id. The body is checked against sectionPatchShape first, which
// has no kind: a section's kind never changes, so "kind" is an unknown field (400).
func (h sectionHandler) update(c *gin.Context) {
	id, ok := sectionID(c)
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
	if !decodeStrict(c, body, &sectionPatchShape{}) {
		return
	}
	var patch sectionPatch
	if err := json.Unmarshal(body, &patch); err != nil {
		// Can't happen for a body that passed the check above, so it would be our bug (500).
		writeServiceError(c, h.logger, fmt.Errorf("decode section patch: %w", err))
		return
	}

	section, err := h.service.Update(c.Request.Context(), id, version, func(in *event.SectionInput) error {
		patch.apply(in)
		return nil
	})
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	writeSection(c, http.StatusOK, section)
}

// delete handles DELETE /api/v1/admin/sections/:id of the version named in If-Match, and answers
// 204 No Content. Its seats are deleted with it. Unlike deleting a hall or a layout, it needs
// the layout to be a draft.
func (h sectionHandler) delete(c *gin.Context) {
	id, ok := sectionID(c)
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

// sectionRequest is the JSON body of POST /layouts/{id}/sections. capacity is for a standing
// section only, and position is optional: without it the section goes after the last one.
type sectionRequest struct {
	Name     string            `json:"name"`
	Kind     event.SectionKind `json:"kind"`
	Capacity *int              `json:"capacity"`
	Position *int              `json:"position"`
}

func (r sectionRequest) toInput() event.SectionInput {
	return event.SectionInput{Name: r.Name, Kind: r.Kind, Capacity: r.Capacity, Position: r.Position}
}

// sectionPatchShape is the shape a PATCH /sections/{id} body is checked against: the fields that
// can change, with their types. kind isn't one of them.
type sectionPatchShape struct {
	Name     string `json:"name"`
	Capacity *int   `json:"capacity"`
	Position *int   `json:"position"`
}

// sectionPatch is the JSON body of PATCH /sections/{id}: any subset of the changeable fields,
// with the three states of patchField (left out, null, a value).
type sectionPatch struct {
	Name     patchField[string] `json:"name"`
	Capacity patchField[int]    `json:"capacity"`
	Position patchField[int]    `json:"position"`
}

// apply changes in, the section's current values, by the patch. null clears capacity, which the
// service accepts only for a seated section; a null name becomes "" and a null position nil,
// which the service reports as "is required".
func (p sectionPatch) apply(in *event.SectionInput) {
	setRequired(p.Name, &in.Name)
	setOptional(p.Capacity, &in.Capacity)
	setOptional(p.Position, &in.Position)
}

// sectionResponse is the JSON form of a section. capacity is null for a seated section.
type sectionResponse struct {
	ID        uuid.UUID         `json:"id"`
	LayoutID  uuid.UUID         `json:"layout_id"`
	Name      string            `json:"name"`
	Kind      event.SectionKind `json:"kind"`
	Capacity  *int              `json:"capacity"`
	Position  int               `json:"position"`
	Version   int               `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func toSectionResponse(s event.Section) sectionResponse {
	return sectionResponse{
		ID:        s.ID,
		LayoutID:  s.LayoutID,
		Name:      s.Name,
		Kind:      s.Kind,
		Capacity:  s.Capacity,
		Position:  s.Position,
		Version:   s.Version,
		CreatedAt: s.CreatedAt.UTC(),
		UpdatedAt: s.UpdatedAt.UTC(),
	}
}

// sectionListResponse is the JSON form of a layout's sections: {"sections": [...]}.
type sectionListResponse struct {
	Sections []sectionResponse `json:"sections"`
}
