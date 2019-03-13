package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

type ResourceType string
type Resources map[ResourceSlug]string

func (r Resources) MarshalJSON() ([]byte, error) {
	res := make(map[string]string)
	for k, v := range r {
		newKey := fmt.Sprintf("%s/%s", k.Namespace, k.Name)
		res[newKey] = v
	}

	return json.MarshalIndent(res, "", "    ")
}

type ResourceSlug struct {
	Type      ResourceType `json:",string"`
	Name      string       `json:""`
	Namespace string	   `json:""`
}

func (s *ResourceSlug) String() string {
	return fmt.Sprintf("%s/%s.%s", s.Type, s.Namespace, s.Name)
}

type Resource struct {
	Slug ResourceSlug `json:""`
	Data string       `json:""`
}

// created and deleted are just hypothetical channel names... not really sure how the kubeapi server event stream
// works (and it's 4am so I don't feel like investigating). Let's pretend for hypothetical sake it works by opening an
// http connection and then whenever a specific watch changes it invokes a callback function. In that callback function
// we would would publish to the created or deleted channel (or updated, but I didn't feel like implementing that for
// the sim.
func ResourceManager(wg *sync.WaitGroup,
	created <-chan *Resource,
	deleted <-chan *ResourceSlug,
	subscribers []chan <- map[ResourceType]Resources) {

	defer wg.Done()

	records := make(map[ResourceType]Resources)

	notifySubscribers := func(data map[ResourceType]Resources) {
		for _, s := range subscribers {
			s <- data
		}
	}

	for {
		select {
		case resource := <-created:
			if _, typeExists := records[resource.Slug.Type]; !typeExists {
				records[resource.Slug.Type] = make(Resources)
			}

			if _, exists := records[resource.Slug.Type][resource.Slug]; !exists {
				records[resource.Slug.Type][resource.Slug] = resource.Data
				fmt.Printf("created resource: %s\n", resource.Slug)
				notifySubscribers(copyMap(records))
			}
		case slug := <-deleted:
			if resourceType, typeExists := records[slug.Type]; typeExists {
				if _, resourceExists := resourceType[*slug]; resourceExists {
					delete(resourceType, *slug)
					fmt.Printf("deleted resource: %s\n", slug)
					notifySubscribers(copyMap(records))
				}
			}
		}
	}
}

// it occurs to me that an easier mechanism than this is to just marshal into JSON before sending to a subscriber and
// then a subscriber can unmarshal.
func copyMap(in map[ResourceType]Resources) map[ResourceType]Resources {
	res := make(map[ResourceType]Resources)

	for resourceType, resources := range in {
		res[resourceType] = make(Resources)
		m := res[resourceType]
		for slug, resource := range resources {
			m[slug] = resource
		}
	}

	return res
}

func subscriber(name string, wg *sync.WaitGroup, notifications chan map[ResourceType]Resources) {
	defer wg.Done()

	for {
		select {
		case n := <- notifications:
			jsonBytes, err := json.MarshalIndent(n, "", "    ")
			if err != nil {
				fmt.Println(err)
			}

			fmt.Printf("--- BEGIN Resources (subscriber-name: %q) ---\n%s\n--- END   Resources ---\n", name, string(jsonBytes))
		}
	}
}

func main() {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	wg := &sync.WaitGroup{}

	sub0Ch := make(chan map[ResourceType]Resources)
	wg.Add(1)
	go subscriber("subscriber-0", wg, sub0Ch)

	sub1Ch := make(chan map[ResourceType]Resources)
	wg.Add(1)
	go subscriber("subscriber-1", wg, sub1Ch)

	created := make(chan *Resource)
	deleted := make(chan *ResourceSlug)

	subscribers := []chan<- map[ResourceType]Resources{sub0Ch, sub1Ch}

	// plug in more simulators as desired
	launchSimulators(wg, []simConfig{
		{wg, r, 1 * time.Second, created, deleted, serviceResourceGenerator("default", "hello")},
		{wg, r, 2 * time.Second, created, deleted, deploymentResourceGenerator("default", "hello-api-server")},
	})

	wg.Add(1)
	go ResourceManager(wg, created, deleted, subscribers)

	wg.Wait()
	fmt.Println("Completed")
}

// =====================================
// Simulator Code
// =====================================

type event struct {
	name   string
	weight int
}

type simConfig struct {
	wg *sync.WaitGroup
	rand *rand.Rand
	tickSpeed time.Duration
	created chan<- *Resource
	deleted chan<- *ResourceSlug
	newResource func() Resource
}

func randomWeightedSelect(rand *rand.Rand, events []event) event {
	var totalWeight int
	for _, e := range events {
		totalWeight += e.weight
	}

	r := rand.Intn(totalWeight)
	for _, e := range events {
		r = r - e.weight
		if r <= 0 {
			return e
		}
	}

	return events[0] // will obviously fail if there are no events...
}

func randAlphanum(n int) string {
	var letters = []rune("0123456789abcdefghijklmnopqrstuvwxyz")
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[r.Intn(len(letters))]
	}
	return string(b)
}

func launchSimulators(wg *sync.WaitGroup, configs []simConfig) {
	for _, c := range configs {
		wg.Add(1)
		go kubeAPISimulator(wg, c)
	}
}

func serviceResourceGenerator(namespace string, baseName string) func() Resource {
	return func() Resource {
		slug := ResourceSlug{
			Type: "v1.Service",
			Name: fmt.Sprintf("%s-%s", baseName, randAlphanum(6)),
			Namespace: namespace,
		}

		return Resource{Slug: slug, Data: "{Pretend This Is Kubernetes YAML or JSON Data}"}
	}
}

func deploymentResourceGenerator(namespace string, baseName string) func() Resource {
	return func() Resource {
		slug := ResourceSlug{
			Type: "apps/v1.Deployment",
			Name: fmt.Sprintf("%s-%s", baseName, randAlphanum(6)),
			Namespace: namespace,
		}

		return Resource{Slug: slug, Data: "{Pretend This Is Kubernetes YAML or JSON Data}"}
	}
}

// Simulate events coming from watches against the Kubernetes API. I do not have time to grok the Kubernetes event
// stream right now but this should get the point across.
//
// tickSpeed - how fast to do stuff
// created   - send an event to the created channel
// deleted   - send an event to the deleted channel
// newResource - create a new Resource as part of the simulation.
//
func kubeAPISimulator(wg *sync.WaitGroup, config simConfig) {
	defer wg.Done()

	ticker := time.NewTicker(config.tickSpeed).C

	events := []event{
		{name: "create", weight: 5},
		{name: "delete", weight: 5},
	}

	createdResources := make([]Resource, 0)
	for {
		select {
		case <- ticker:
			ev := event{name: "create"}
			if len(createdResources) != 0 {
				ev = randomWeightedSelect(config.rand, events)
			}

			switch ev.name {
			case "create":
				r := config.newResource()
				createdResources = append(createdResources, r)
				config.created <- &r
			case "delete":
				front := createdResources[0]
				createdResources = createdResources[1:]
				config.deleted <- &front.Slug
			}
		}
	}
}