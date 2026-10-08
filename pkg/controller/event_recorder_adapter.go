package controller

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/tools/record"
)

type eventRecorderAdapter struct {
	recorder events.EventRecorder
}

// NewEventRecorderAdapter adapts the current Kubernetes events recorder API to
// the legacy recorder interface used by the reconcilers.
func NewEventRecorderAdapter(recorder events.EventRecorder) record.EventRecorder {
	return eventRecorderAdapter{recorder: recorder}
}

func (a eventRecorderAdapter) Event(object runtime.Object, eventtype, reason, message string) {
	a.recorder.Eventf(object, nil, eventtype, reason, reason, message)
}

func (a eventRecorderAdapter) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	a.recorder.Eventf(object, nil, eventtype, reason, reason, messageFmt, args...)
}

func (a eventRecorderAdapter) AnnotatedEventf(object runtime.Object, _ map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
	a.Eventf(object, eventtype, reason, messageFmt, args...)
}
