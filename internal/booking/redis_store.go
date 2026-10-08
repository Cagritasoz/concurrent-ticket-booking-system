package booking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore implements session based seat booking backed by Redis.
//
// Key design:
//
// seat:{movieID}:{seatID} -> session JSON (TTL = held, no TTL = confirmed)
// {}'s are placeholders.
// session:{sessionID} -> seat key (reverse lookup)
// *redis.Client, redis is the package name and Client is a type exported by that package.

type RedisStore struct {
	rdb *redis.Client
}

const defaultHoldTTL = 3 * time.Minute // 3m0s = 3 minutes

func NewRedisStore(rdb *redis.Client) *RedisStore {
	return &RedisStore{rdb: rdb}
}

func seatKey(movieID string, seatID string) string {
	return fmt.Sprintf("seat:%s:%s", movieID, seatID)
}

// sessionKey builds the reverse-lookup key for a session.
func sessionKey(sessionID string) string {
	return fmt.Sprintf("session:%s", sessionID)
}

func (s *RedisStore) Book(b Booking) error {
	_, err := s.hold(b)
	if err != nil {
		return err
	}
	return nil
}

func (s *RedisStore) ListBookings(movieID string) []Booking {
	//TODO implement me
	panic("implement me")
}

func (s *RedisStore) hold(b Booking) (Booking, error) {
	// b.ID is the session ID. If it's empty every hold would share the key "session:" and overwrite each other.
	if b.ID == "" {
		return Booking{}, ErrMissingSessionID
	}

	now := time.Now()

	ctx := context.Background()

	held := Booking{
		ID:        b.ID,
		MovieID:   b.MovieID,
		SeatID:    b.SeatID,
		UserID:    b.UserID,
		Status:    "held",
		ExpiresAt: now.Add(defaultHoldTTL),
	}

	value, err := encodeBooking(held)
	if err != nil {
		return Booking{}, err
	}

	seat := seatKey(b.MovieID, b.SeatID)

	// NX: only set if the key doesn't exist yet, TTL: Redis deletes the key when the hold expires.
	// Both happen in one atomic command, so exactly one goroutine can win the seat.
	// The value parameter of SetArgs has type interface{} which is like the Object class in Java, takes any type.
	err = s.rdb.SetArgs(ctx, seat, value, redis.SetArgs{
		Mode: "NX",
		TTL:  defaultHoldTTL,
	}).Err()

	// errors.Is works recursively, reports whether any error in err's tree matches target
	if errors.Is(err, redis.Nil) { // redis.Nil means NX failed: the key already exists, the seat is taken by another.
		return Booking{}, ErrSeatAlreadyBooked
	}
	if err != nil { // Another error that is not redis.Nil
		return Booking{}, fmt.Errorf("hold seat %s: %w", seat, err)
	}

	// Reverse lookup: session -> seat key. Stores the full seat key (not just SeatID) so the seat can be found
	// without knowing the movie. Same TTL as the hold, so both expire together.
	if err := s.rdb.Set(ctx, sessionKey(b.ID), seat, defaultHoldTTL).Err(); err != nil {

		// The seat is already held but the session key failed, undo the hold so the seat isn't stuck for the TTL.
		// Uses a fresh context so the cleanup still runs if ctx was canceled.
		if delErr := s.rdb.Del(context.Background(), seat).Err(); delErr != nil {
			return Booking{}, fmt.Errorf("save session %s: %w", b.ID, errors.Join(err, delErr))
		}
		return Booking{}, fmt.Errorf("save session %s: %w", b.ID, err)
	}

	return held, nil
}

// encodeBooking turns a Booking into JSON bytes so it can be stored as a Redis string value.
func encodeBooking(b Booking) ([]byte, error) {
	data, err := json.Marshal(b)
	if err != nil {
		// The original error is kept inside the new one, like an exception's cause.
		return nil, fmt.Errorf("encode booking: %w", err) // %w is a special verb that only works for errors.
	}
	return data, nil
}
