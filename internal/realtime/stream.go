// Package realtime implements cursor-correct delivery where a live bus is
// only a wake-up accelerator and PostgreSQL EventStore remains authoritative.
package realtime

import (
	"context"
	"errors"
	"time"

	realtimepostgres "github.com/langshift/lites/internal/realtime/postgres"
)

var (
	ErrConfiguration = errors.New("realtime stream is not configured")
	ErrCursorAhead   = errors.New("realtime cursor is ahead of the EventStore")
	ErrEventGap      = errors.New("realtime EventStore sequence has a gap")
	ErrAuthExpired   = errors.New("realtime authorization expired")
)

type EventStore interface {
	HighWatermark(context.Context, string, string) (uint64, error)
	List(context.Context, string, string, uint64, uint64, int) ([]realtimepostgres.Event, error)
}

type WakeSubscription interface {
	Notifications() <-chan struct{}
	Close() error
}

type WakeSource interface {
	Subscribe(context.Context, string, string) (WakeSubscription, error)
}

type Sink interface {
	Event(context.Context, realtimepostgres.Event) error
	Control(context.Context, Control) error
}

type Control struct {
	Kind       string `json:"kind"`
	Cursor     uint64 `json:"cursor"`
	RetryAfter int64  `json:"retry_after_ms,omitempty"`
}

type Session struct {
	TenantID, UserID string
	AfterSequence    uint64
	ExpiresAt        time.Time
}

type Stream struct {
	Store           EventStore
	Wakes           WakeSource
	PageSize        int
	Heartbeat       time.Duration
	CatchUpInterval time.Duration
	WakeRetry       time.Duration
	SendTimeout     time.Duration
	ReauthLead      time.Duration
	Now             func() time.Time
	Metrics         Metrics
}

type Metrics interface {
	AddRealtimeConnections(context.Context, int64)
	ObserveRealtimeGapRecovery(context.Context, time.Duration)
}

func (stream Stream) Run(ctx context.Context, session Session, sink Sink) error {
	if stream.Store == nil || stream.Wakes == nil || sink == nil || session.TenantID == "" || session.UserID == "" || session.ExpiresAt.IsZero() || stream.PageSize < 1 || stream.PageSize > 1000 || stream.Heartbeat <= 0 || stream.CatchUpInterval <= 0 || stream.WakeRetry <= 0 || stream.SendTimeout <= 0 || stream.ReauthLead < 0 {
		return ErrConfiguration
	}
	now := stream.now()
	if !session.ExpiresAt.After(now) {
		return ErrAuthExpired
	}
	if stream.Metrics != nil {
		stream.Metrics.AddRealtimeConnections(ctx, 1)
		defer stream.Metrics.AddRealtimeConnections(ctx, -1)
	}
	subscription, notifications := stream.subscribe(ctx, session)
	defer func() {
		if subscription != nil {
			_ = subscription.Close()
		}
	}()
	cursor, err := stream.catchUp(ctx, session, sink, session.AfterSequence)
	if err != nil {
		return err
	}
	if err = stream.sendControl(ctx, sink, Control{Kind: "ready", Cursor: cursor}, session.ExpiresAt); err != nil {
		return err
	}
	heartbeat := time.NewTicker(stream.Heartbeat)
	defer heartbeat.Stop()
	catchUp := time.NewTicker(stream.CatchUpInterval)
	defer catchUp.Stop()
	retryWake := time.NewTicker(stream.WakeRetry)
	defer retryWake.Stop()
	authTimer := time.NewTimer(session.ExpiresAt.Sub(stream.now()))
	defer authTimer.Stop()
	reauthDelay := session.ExpiresAt.Add(-stream.ReauthLead).Sub(stream.now())
	if reauthDelay < 0 {
		reauthDelay = 0
	}
	reauthTimer := time.NewTimer(reauthDelay)
	defer reauthTimer.Stop()
	reauthSent := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, open := <-notifications:
			if !open {
				if subscription != nil {
					_ = subscription.Close()
				}
				subscription, notifications = nil, nil
			}
			cursor, err = stream.catchUp(ctx, session, sink, cursor)
			if err != nil {
				return err
			}
		case <-catchUp.C:
			cursor, err = stream.catchUp(ctx, session, sink, cursor)
			if err != nil {
				return err
			}
		case <-retryWake.C:
			if subscription == nil {
				subscription, notifications = stream.subscribe(ctx, session)
			}
		case <-heartbeat.C:
			if err = stream.sendControl(ctx, sink, Control{Kind: "heartbeat", Cursor: cursor}, session.ExpiresAt); err != nil {
				return err
			}
		case <-reauthTimer.C:
			if !reauthSent {
				reauthSent = true
				if err = stream.sendControl(ctx, sink, Control{Kind: "reauth_required", Cursor: cursor}, session.ExpiresAt); err != nil {
					return err
				}
			}
		case <-authTimer.C:
			return ErrAuthExpired
		}
	}
}

func (stream Stream) catchUp(ctx context.Context, session Session, sink Sink, cursor uint64) (uint64, error) {
	catchCtx, cancel := context.WithDeadline(ctx, session.ExpiresAt)
	defer cancel()
	high, err := stream.Store.HighWatermark(catchCtx, session.TenantID, session.UserID)
	if err != nil {
		if errors.Is(catchCtx.Err(), context.DeadlineExceeded) {
			return cursor, ErrAuthExpired
		}
		return cursor, err
	}
	if cursor > high {
		return cursor, ErrCursorAhead
	}
	recoveryStarted := stream.now()
	recovering := cursor < high
	for cursor < high {
		events, listErr := stream.Store.List(catchCtx, session.TenantID, session.UserID, cursor, high, stream.PageSize)
		if listErr != nil {
			if errors.Is(catchCtx.Err(), context.DeadlineExceeded) {
				return cursor, ErrAuthExpired
			}
			return cursor, listErr
		}
		if len(events) == 0 {
			return cursor, ErrEventGap
		}
		for _, event := range events {
			if event.Sequence != cursor+1 || event.Sequence > high {
				return cursor, ErrEventGap
			}
			sendCtx, sendCancel := context.WithTimeout(catchCtx, stream.SendTimeout)
			sendErr := sink.Event(sendCtx, event)
			sendCancel()
			if sendErr != nil {
				if errors.Is(catchCtx.Err(), context.DeadlineExceeded) {
					return cursor, ErrAuthExpired
				}
				return cursor, sendErr
			}
			cursor = event.Sequence
		}
	}
	if recovering && stream.Metrics != nil {
		stream.Metrics.ObserveRealtimeGapRecovery(ctx, stream.now().Sub(recoveryStarted))
	}
	return cursor, nil
}

func (stream Stream) sendControl(ctx context.Context, sink Sink, control Control, expiresAt time.Time) error {
	if !expiresAt.After(stream.now()) {
		return ErrAuthExpired
	}
	authCtx, authCancel := context.WithDeadline(ctx, expiresAt)
	defer authCancel()
	sendCtx, sendCancel := context.WithTimeout(authCtx, stream.SendTimeout)
	defer sendCancel()
	err := sink.Control(sendCtx, control)
	if errors.Is(authCtx.Err(), context.DeadlineExceeded) {
		return ErrAuthExpired
	}
	return err
}

func (stream Stream) subscribe(ctx context.Context, session Session) (WakeSubscription, <-chan struct{}) {
	subscription, err := stream.Wakes.Subscribe(ctx, session.TenantID, session.UserID)
	if err != nil || subscription == nil || subscription.Notifications() == nil {
		return nil, nil
	}
	return subscription, subscription.Notifications()
}

func (stream Stream) now() time.Time {
	if stream.Now != nil {
		return stream.Now().UTC()
	}
	return time.Now().UTC()
}
