package events

import "github.com/google/uuid"

// subjectPrefix namespaces every wzap event subject.
const subjectPrefix = "wzap.instances."

// SubjectNamespace builds the JetStream subjects of an instance. It carries no
// state; call the methods on the Subjects value.
type SubjectNamespace struct{}

// Subjects builds the broker subjects of an instance, for example
// Subjects.Connection(id) -> "wzap.instances.<id>.connection".
var Subjects SubjectNamespace

// Connection is the subject of the instance connection-state events.
func (SubjectNamespace) Connection(instanceID uuid.UUID) string {
	return subject(instanceID, "connection")
}

// Message is the subject of the inbound message events.
func (SubjectNamespace) Message(instanceID uuid.UUID) string {
	return subject(instanceID, "message")
}

// Receipt is the subject of the inbound receipt events.
func (SubjectNamespace) Receipt(instanceID uuid.UUID) string {
	return subject(instanceID, "receipt")
}

// MessageStatus is the subject of the outbound message status events.
func (SubjectNamespace) MessageStatus(instanceID uuid.UUID) string {
	return subject(instanceID, "message.status")
}

func subject(instanceID uuid.UUID, suffix string) string {
	return subjectPrefix + instanceID.String() + "." + suffix
}
