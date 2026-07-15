package guest

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

type Server struct {
	Executor              Executor
	MaximumConcurrent     int
	InitialRequestTimeout time.Duration
	WriteTimeout          time.Duration
}

func (server Server) Serve(ctx context.Context, listener net.Listener) error {
	if listener == nil || server.MaximumConcurrent < 1 || server.MaximumConcurrent > 64 || server.InitialRequestTimeout <= 0 || server.InitialRequestTimeout > 30*time.Second || server.WriteTimeout <= 0 || server.WriteTimeout > 30*time.Second {
		return ErrExecutionPolicy
	}
	serveContext, cancel := context.WithCancel(ctx)
	var active sync.WaitGroup
	defer func() {
		cancel()
		active.Wait()
	}()
	go func() {
		<-serveContext.Done()
		_ = listener.Close()
	}()
	semaphore := make(chan struct{}, server.MaximumConcurrent)
	for {
		select {
		case semaphore <- struct{}{}:
		case <-serveContext.Done():
			return nil
		}
		connection, err := listener.Accept()
		if err != nil {
			<-semaphore
			if serveContext.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		active.Add(1)
		go func() {
			defer active.Done()
			defer func() { <-semaphore }()
			_ = server.ServeConnection(serveContext, connection)
		}()
	}
}

func (server Server) ServeConnection(ctx context.Context, connection net.Conn) error {
	if connection == nil || server.InitialRequestTimeout <= 0 || server.InitialRequestTimeout > 30*time.Second || server.WriteTimeout <= 0 || server.WriteTimeout > 30*time.Second {
		return ErrExecutionPolicy
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(server.InitialRequestTimeout)); err != nil {
		return err
	}
	frame, err := NewDecoder(connection).Decode()
	if err != nil {
		return err
	}
	if frame.Kind != FrameExecute || frame.Execute == nil {
		return ErrMalformedFrame
	}
	if err = connection.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	encoder := NewEncoder(connection)
	_, err = server.Executor.Execute(ctx, frame.RequestID, *frame.Execute, func(output Frame) error {
		if deadlineErr := connection.SetWriteDeadline(time.Now().Add(server.WriteTimeout)); deadlineErr != nil {
			return deadlineErr
		}
		return encoder.Encode(output)
	})
	return err
}
