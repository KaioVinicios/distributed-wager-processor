package sqsconsumer

import "github.com/aws/aws-sdk-go-v2/service/sqs/types"

// groupBatch splits a received batch by MessageGroupId, keeping the order of
// arrival inside each group and the order of first appearance between groups
// (messaging.md §4.2).
func groupBatch(msgs []types.Message) [][]types.Message {
	index := map[string]int{}
	var groups [][]types.Message
	for _, m := range msgs {
		g := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
		i, ok := index[g]
		if !ok {
			i = len(groups)
			index[g] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], m)
	}
	return groups
}
