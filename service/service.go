// Package service contains multithreaded worker pool implementation.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"go.ectobit.com/lax"
	"go.ectobit.com/oxeye/broker"
	"go.ectobit.com/oxeye/encdec"
)

// Errors.
var (
	ErrInvalidMessageType = errors.New("invalid message type")
)

// Job defines common job methods.
type Job interface {
	Execute(msg interface{}) (interface{}, error)
	NewInMessage() interface{}
}

// Service is a multithreaded service with configurable job to be executed.
type Service struct {
	concurrency uint8
	broker      broker.Broker
	wg          sync.WaitGroup
	job         Job
	ed          encdec.EncDecoder
	log         lax.Logger
}

// NewService creates new service.
func NewService(concurrency uint8, broker broker.Broker, job Job, ed encdec.EncDecoder, log lax.Logger) *Service {
	return &Service{ //nolint:exhaustivestruct
		concurrency: concurrency,
		broker:      broker,
		job:         job,
		ed:          ed,
		log:         log,
	}
}

// Run executes service reacting on termination signals for graceful shutdown.
func (s *Service) Run() error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.log.Info("starting worker pool", lax.Uint8("concurrency", s.concurrency))

	sub, err := s.broker.Sub(ctx)
	if err != nil {
		return fmt.Errorf("broker: %w", err)
	}

	for workerID := uint8(1); workerID <= s.concurrency; workerID++ {
		go s.run(ctx, workerID, sub)
	}

	<-signals
	s.log.Info("graceful shutdown")
	s.wg.Wait()

	return nil
}

func (s *Service) run(ctx context.Context, workerID uint8, messages <-chan broker.Message) {
	s.log.Info("started", lax.Uint8("worker", workerID))
	s.wg.Add(1)

	for {
		select {
		case msg := <-messages:
			s.log.Debug("executing", lax.Uint8("worker", workerID))

			inMsg := s.job.NewInMessage()

			if err := s.ed.Decode(msg.Data, inMsg); err != nil {
				s.log.Warn("decode", lax.String("type", fmt.Sprintf("%T", inMsg)),
					lax.Uint8("worker", workerID), lax.Error(err))

				continue
			}

			outMsg, err := s.job.Execute(inMsg)
			if err != nil {
				s.log.Warn("execute", lax.Error(err))

				continue
			}

			out, err := s.ed.Encode(outMsg)
			if err != nil {
				s.log.Warn("encode", lax.String("type", fmt.Sprintf("%T", outMsg)),
					lax.Uint8("worker", workerID), lax.Error(err))

				continue
			}

			if err := s.broker.Pub(out); err != nil {
				s.log.Warn("broker", lax.Uint8("worker", workerID), lax.Error(err))
			}

			msg.Ack()
		case <-ctx.Done():
			s.log.Info("stopped", lax.Uint8("worker", workerID))
			s.wg.Done()

			return
		}
	}
}

// Exit exits CLI application writing message and error to stderr.
func Exit(message string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v", message, err)
	os.Exit(1)
}
