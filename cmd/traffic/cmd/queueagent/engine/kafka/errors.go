package kafka

import (
	"context"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// kerrCode reports whether err is, or wraps, a *kerr.Error with the given
// code.
func kerrCode(err error, code int16) bool {
	var ke *kerr.Error
	if !errors.As(err, &ke) {
		return false
	}
	return ke.Code == code
}

// isFencedProducerErr reports whether err is the broker's signal that a
// newer producer holds this transactional ID's epoch: INVALID_PRODUCER_EPOCH
// from a produce, or PRODUCER_FENCED from an end-transaction.
func isFencedProducerErr(err error) bool {
	return kerrCode(err, kerr.InvalidProducerEpoch.Code) || kerrCode(err, kerr.ProducerFenced.Code)
}

// isFencedInstanceErr reports whether err is FENCED_INSTANCE_ID: a
// replacement consumer has joined the splitter group under this instance's
// static group.instance.id.
func isFencedInstanceErr(err error) bool {
	return kerrCode(err, kerr.FencedInstanceID.Code)
}

// isUnknownMemberErr reports whether err is UNKNOWN_MEMBER_ID: the broker
// refuses an external offset commit against a group that currently has a
// live member.
func isUnknownMemberErr(err error) bool {
	return kerrCode(err, kerr.UnknownMemberID.Code)
}

// isTopicAlreadyExistsErr reports whether err is TOPIC_ALREADY_EXISTS.
func isTopicAlreadyExistsErr(err error) bool {
	return kerrCode(err, kerr.TopicAlreadyExists.Code)
}

// isUnknownTopicErr reports whether err is UNKNOWN_TOPIC_OR_PARTITION.
func isUnknownTopicErr(err error) bool {
	return kerrCode(err, kerr.UnknownTopicOrPartition.Code)
}

// isUnknownGroupErr reports whether err is GROUP_ID_NOT_FOUND.
func isUnknownGroupErr(err error) bool {
	return kerrCode(err, kerr.GroupIDNotFound.Code)
}

// isOrderlyShutdownErr reports whether err is the shape an orderly Stop
// produces once its poll context is canceled: context cancellation itself,
// or the kgo client reporting it is already closed.
func isOrderlyShutdownErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, kgo.ErrClientClosed)
}

// handoffRefusedErr wraps UNKNOWN_MEMBER_ID from a handoff offset commit
// with an actionable message: the broker refuses the commit while the
// original application group still has a live member.
func handoffRefusedErr(group string, err error) error {
	return fmt.Errorf("kafka: cannot hand back source ownership: consumer group %q still has active members: %w", group, err)
}
