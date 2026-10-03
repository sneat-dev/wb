package sessionrun

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmessenger"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"os"
	"strings"
	"time"
)

type MessageRequest struct {
	ProjectsRoot, Target, Body, ResumeID, RetryVerb string
	Kind                                            sessionmove.MessageKind
}
type MessageDependencies struct {
	ResolveSource func(string) (session.Record, bool, error)
	Store         func(string) (sessionmove.Store, error)
	NewMessageID  func() (string, error)
	Send          func(context.Context, sessionmessenger.Options) (sessionmessenger.Result, error)
	Now           func() time.Time
}

func ResolveSource(projectsRoot string) (session.Record, bool, error) {
	directory, err := DirForRead(projectsRoot)
	if err != nil {
		return session.Record{}, false, err
	}
	record, ok := session.ResolveForProcess(directory, os.Getpid())
	return record, ok, nil
}
func DefaultMessageDependencies() MessageDependencies {
	return MessageDependencies{ResolveSource: ResolveSource, Store: MoveStore, NewMessageID: sessionmove.NewMessageID, Send: sessionmessenger.Send, Now: func() time.Time { return time.Now().UTC() }}
}

type MessageService struct{ deps MessageDependencies }

func NewMessage(deps MessageDependencies) *MessageService {
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	return &MessageService{deps: deps}
}
func (s *MessageService) Send(ctx context.Context, request MessageRequest) (sessionmessenger.Result, error) {
	deps := s.deps
	projectsRoot, target, kind, body, resume, retryVerb := request.ProjectsRoot, request.Target, request.Kind, request.Body, request.ResumeID, request.RetryVerb
	target = strings.TrimSpace(target)
	if target == "" {
		return sessionmessenger.Result{}, fmt.Errorf("successor WB session ID is required")
	}
	source, ok, err := deps.ResolveSource(projectsRoot)
	if err != nil {
		return sessionmessenger.Result{}, err
	}
	if !ok {
		return sessionmessenger.Result{}, fmt.Errorf("session messaging requires the live registered predecessor session that owns this process")
	}
	store, err := deps.Store(projectsRoot)
	if err != nil {
		return sessionmessenger.Result{}, err
	}
	messageID := ""
	if resume == "" {
		messageID, err = deps.NewMessageID()
		if err != nil {
			return sessionmessenger.Result{}, err
		}
	}
	result, err := deps.Send(ctx, sessionmessenger.Options{
		Store: store, ProjectsRoot: projectsRoot, TargetWBSessionID: target, SourceSession: source,
		Kind: kind, Body: body, MessageID: messageID, ResumeMessageID: resume, Now: deps.Now,
	})
	if err != nil {
		var deliveryErr *sessionmessenger.DeliveryError
		if errors.As(err, &deliveryErr) && deliveryErr.MessageID != "" {
			return sessionmessenger.Result{}, fmt.Errorf("%w; retry the exact durable bytes with: wb session %s %s --resume %s",
				err, retryVerb, target, deliveryErr.MessageID)
		}
		return sessionmessenger.Result{}, err
	}
	return result, nil
}
