package nodes

import (
	"log"
	"os"
)

type Node struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
	IP       string `json:"ip"`
	URL      string `json:"-"`
	Secret   string `json:"-"`
}

var Secret = os.Getenv("AGENT_SECRET") // set via AGENT_SECRET env var

func init() {
	if Secret == "" {
		log.Fatal("nodes: AGENT_SECRET environment variable is not set")
	}
}

var List = []Node{
	{
		ID:       "node1",
		Name:     "City — ISP",
		Location: "City",
		//ISP:      "ISP Name",
		IP:  "0.0.0.0",
		URL: "http://127.0.0.1:9090",
	},
	{
		ID:       "node1",
		Name:     "City — ISP",
		Location: "City",
		//ISP:      "ISP Name",
		IP:  "0.0.0.0",
		URL: "http://127.0.0.1:9090",
	},
	{
		ID:       "node1",
		Name:     "City — ISP",
		Location: "City",
		//ISP:      "ISP Name",
		IP:  "0.0.0.0",
		URL: "http://127.0.0.1:9090",
	},
	{
		ID:       "node1",
		Name:     "City — ISP",
		Location: "City",
		//ISP:      "ISP Name",
		IP:  "0.0.0.0",
		URL: "http://127.0.0.1:9090",
	},
}
