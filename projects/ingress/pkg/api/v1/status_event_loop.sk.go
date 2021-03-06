// Code generated by solo-kit. DO NOT EDIT.

package v1

import (
	"context"

	"go.opencensus.io/trace"

	"github.com/hashicorp/go-multierror"

	"github.com/solo-io/go-utils/contextutils"
	"github.com/solo-io/go-utils/errutils"
	"github.com/solo-io/solo-kit/pkg/api/v1/clients"
	"github.com/solo-io/solo-kit/pkg/api/v1/eventloop"
	"github.com/solo-io/solo-kit/pkg/errors"
)

type StatusSyncer interface {
	Sync(context.Context, *StatusSnapshot) error
}

type StatusSyncers []StatusSyncer

func (s StatusSyncers) Sync(ctx context.Context, snapshot *StatusSnapshot) error {
	var multiErr *multierror.Error
	for _, syncer := range s {
		if err := syncer.Sync(ctx, snapshot); err != nil {
			multiErr = multierror.Append(multiErr, err)
		}
	}
	return multiErr.ErrorOrNil()
}

type statusEventLoop struct {
	emitter StatusSnapshotEmitter
	syncer  StatusSyncer
	ready   chan struct{}
}

func NewStatusEventLoop(emitter StatusSnapshotEmitter, syncer StatusSyncer) eventloop.EventLoop {
	return &statusEventLoop{
		emitter: emitter,
		syncer:  syncer,
		ready:   make(chan struct{}),
	}
}

func (el *statusEventLoop) Ready() <-chan struct{} {
	return el.ready
}

func (el *statusEventLoop) Run(namespaces []string, opts clients.WatchOpts) (<-chan error, error) {
	opts = opts.WithDefaults()
	opts.Ctx = contextutils.WithLogger(opts.Ctx, "v1.event_loop")
	logger := contextutils.LoggerFrom(opts.Ctx)
	logger.Infof("event loop started")

	errs := make(chan error)

	watch, emitterErrs, err := el.emitter.Snapshots(namespaces, opts)
	if err != nil {
		return nil, errors.Wrapf(err, "starting snapshot watch")
	}
	go errutils.AggregateErrs(opts.Ctx, errs, emitterErrs, "v1.emitter errors")
	go func() {
		var channelClosed bool
		// create a new context for each loop, cancel it before each loop
		var cancel context.CancelFunc = func() {}
		// use closure to allow cancel function to be updated as context changes
		defer func() { cancel() }()
		for {
			select {
			case snapshot, ok := <-watch:
				if !ok {
					return
				}
				// cancel any open watches from previous loop
				cancel()

				ctx, span := trace.StartSpan(opts.Ctx, "status.ingress.solo.io.EventLoopSync")
				ctx, canc := context.WithCancel(ctx)
				cancel = canc
				err := el.syncer.Sync(ctx, snapshot)
				span.End()

				if err != nil {
					select {
					case errs <- err:
					default:
						logger.Errorf("write error channel is full! could not propagate err: %v", err)
					}
				} else if !channelClosed {
					channelClosed = true
					close(el.ready)
				}
			case <-opts.Ctx.Done():
				return
			}
		}
	}()
	return errs, nil
}
