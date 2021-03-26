package scout

type ScoutReport struct {
	Action             string
	Metadata           map[string]interface{}
	PersistentMetadata map[string]interface{}
}
