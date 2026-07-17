package reminder

import (
	"context"
	"errors"
	"regexp"
	"time"
)

var deliveryErrorCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type Delivery struct {
	TenantID, UserID, DeliveryID, ScheduleID, DeliveryKey string
	Channel, Locale, Title, Body                          string
	OccurrenceAt                                          time.Time
}

// Sender must use DeliveryKey as the downstream idempotency key. A successful
// retry of the same key must not create a second user-visible notification.
type Sender interface {
	Send(context.Context, Delivery) error
}

type SendError struct {
	Code      string
	Permanent bool
	Cause     error
}

func (err SendError) Error() string {
	if err.Cause != nil {
		return err.Code + ": " + err.Cause.Error()
	}
	return err.Code
}

func (err SendError) Unwrap() error { return err.Cause }

func NewSendError(code string, permanent bool, cause error) error {
	if !deliveryErrorCode.MatchString(code) {
		code = "delivery_failed"
	}
	return SendError{Code: code, Permanent: permanent, Cause: cause}
}

func ClassifySendError(err error) (code string, permanent bool) {
	if err == nil {
		return "", false
	}
	var classified SendError
	if errors.As(err, &classified) && deliveryErrorCode.MatchString(classified.Code) {
		return classified.Code, classified.Permanent
	}
	return "delivery_dependency_unavailable", false
}
