package event

type UserCommandAppendRequest struct {
	UserID              string
	Idempotency         Idempotency
	IdempotencyResponse IdempotencyResponse
	Aggregate           *RunAggregate
	Events              []EventDraft
	Commands            []CommandDraft
}

func NewUserCommandAppend(request UserCommandAppendRequest) AppendRequest {
	return AppendRequest{
		Actor:               Actor{Kind: ActorUser},
		UserID:              request.UserID,
		Idempotency:         &request.Idempotency,
		IdempotencyResponse: &request.IdempotencyResponse,
		Aggregate:           request.Aggregate,
		Events:              request.Events,
		Commands:            request.Commands,
	}
}

type WorkerCompletionAppendRequest struct {
	WorkerID  string
	UserID    string
	JobFence  JobFence
	Aggregate *RunAggregate
	Events    []EventDraft
	Effects   []TxEffect
}

func NewWorkerCompletionAppend(request WorkerCompletionAppendRequest) AppendRequest {
	return AppendRequest{
		Actor: Actor{
			Kind: ActorWorker,
			ID:   request.WorkerID,
		},
		UserID:    request.UserID,
		JobFence:  &request.JobFence,
		Aggregate: request.Aggregate,
		Events:    request.Events,
		Effects:   request.Effects,
	}
}

type WorkerAckAppendRequest struct {
	WorkerID string
	UserID   string
	JobFence JobFence
}

func NewWorkerAckAppend(request WorkerAckAppendRequest) AppendRequest {
	return AppendRequest{
		Actor: Actor{
			Kind: ActorWorker,
			ID:   request.WorkerID,
		},
		UserID:   request.UserID,
		JobFence: &request.JobFence,
	}
}

type SystemRunAppendRequest struct {
	CommandID string
	UserID    string
	Aggregate RunAggregate
	Events    []EventDraft
	Effects   []TxEffect
}

func NewSystemRunAppend(request SystemRunAppendRequest) AppendRequest {
	return AppendRequest{
		Actor:     Actor{Kind: ActorSystem},
		UserID:    request.UserID,
		CommandID: request.CommandID,
		Aggregate: &request.Aggregate,
		Events:    request.Events,
		Effects:   request.Effects,
	}
}
