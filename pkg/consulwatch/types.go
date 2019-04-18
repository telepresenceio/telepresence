package consulwatch

import "time"

// Endpoints contains an Array of Endpoint structs and meta information about the Service that the contained endpoints
// are associated with.
type Endpoints struct {
	Id        string     `json:""`
	Service   string     `json:""`
	Endpoints []Endpoint `json:""`
}

// GroupByTags returns a map of tag name to array of Endpoint structs.
func (e *Endpoints) GroupByTags() map[string][]Endpoint {
	result := make(map[string][]Endpoint)

	for _, endpoint := range e.Endpoints {
		for _, tag := range endpoint.Tags {
			if _, found := result[tag]; !found {
				result[tag] = []Endpoint{}
			}

			updatedEndpoints := append(result[tag], endpoint)
			result[tag] = updatedEndpoints
		}
	}

	return result
}

type Endpoint struct {
	SystemID string   `json:""`
	ID       string   `json:""`
	Service  string   `json:""`
	Address  string   `json:""`
	Port     int      `json:""`
	Tags     []string `json:""`
}

type Certificate struct {
	SerialNumber  string    `json:",omitempty"`
	PEM           string    `json:",omitempty"`
	PrivateKeyPEM string    `json:",omitempty"`
	Service       string    `json:",omitempty"`
	ServiceURI    string    `json:",omitempty"`
	ValidAfter    time.Time `json:",omitempty"`
	ValidBefore   time.Time `json:",omitempty"`
}

type CARoot struct {
	ID     string `json:",omitempty"`
	Name   string `json:",omitempty"`
	PEM    string `json:",omitempty"`
	Active bool
}

type CARoots struct {
	ActiveRootID string
	TrustDomain  string
	Roots        map[string]CARoot
}
