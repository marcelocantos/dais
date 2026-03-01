// dais-ctl is a helper binary used by the shepherd to manage Claude Code
// workers via the daisd ctl REST API.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/marcelocantos/dais/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "--version":
		fmt.Println("dais-ctl", cli.Version)
		os.Exit(0)
	case "--help-agent":
		fmt.Print(usageText)
		fmt.Println()
		fmt.Print(cli.AgentGuide)
		os.Exit(0)
	case "create":
		doCreate(os.Args[2:])
	case "list":
		doList()
	case "status":
		requireArg(2, "worker ID")
		doStatus(os.Args[2])
	case "command":
		requireArg(2, "worker ID")
		requireArg(3, "prompt text")
		doCommand(os.Args[2], strings.Join(os.Args[3:], " "))
	case "wait":
		requireArg(2, "worker ID")
		doWait(os.Args[2])
	case "kill":
		requireArg(2, "worker ID")
		doKill(os.Args[2])
	default:
		usage()
	}
}

const usageText = `Usage: dais-ctl <command> [args]

Commands:
  create [--name NAME] [--workdir DIR] [--model MODEL]
      Create a new worker session.

  list
      List all workers and their status.

  status <worker-id>
      Show detailed status and recent output of a worker.

  command <worker-id> <prompt>
      Send a command to a worker (returns immediately).

  wait <worker-id>
      Wait for a worker to finish its current command.

  kill <worker-id>
      Terminate a worker session.

  --version       Print version and exit.
  --help-agent    Print agent guide and exit.
`

func usage() {
	fmt.Fprint(os.Stderr, usageText)
	os.Exit(1)
}

func requireArg(idx int, name string) {
	if len(os.Args) <= idx {
		fmt.Fprintf(os.Stderr, "Error: missing %s\n", name)
		os.Exit(1)
	}
}

func baseURL() string {
	addr := os.Getenv("DAIS_CTL_ADDR")
	if addr == "" {
		addr = "http://localhost:8080"
	}
	return addr
}

func doCreate(args []string) {
	name := ""
	workdir := ""
	model := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			i++
			if i < len(args) {
				name = args[i]
			}
		case "--workdir":
			i++
			if i < len(args) {
				workdir = args[i]
			}
		case "--model":
			i++
			if i < len(args) {
				model = args[i]
			}
		}
	}

	body, _ := json.Marshal(map[string]string{
		"name":    name,
		"workdir": workdir,
		"model":   model,
	})

	resp, err := http.Post(baseURL()+"/ctl/workers", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var result struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	fmt.Printf("Created worker %s (%s)\n", result.Name, result.ID)
}

func doList() {
	resp, err := http.Get(baseURL() + "/ctl/workers")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var workers []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	json.NewDecoder(resp.Body).Decode(&workers)

	if len(workers) == 0 {
		fmt.Println("No active workers.")
		return
	}

	for _, w := range workers {
		fmt.Printf("  %s  %-20s  %s\n", w.ID, w.Name, w.Status)
	}
}

func doStatus(id string) {
	resp, err := http.Get(baseURL() + "/ctl/workers/" + id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintf(os.Stderr, "Worker %s not found.\n", id)
		os.Exit(1)
	}

	var detail struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		LastResult string `json:"last_result"`
	}
	json.NewDecoder(resp.Body).Decode(&detail)

	fmt.Printf("Worker: %s (%s)\n", detail.Name, detail.ID)
	fmt.Printf("Status: %s\n", detail.Status)
	if detail.LastResult != "" {
		fmt.Printf("Last result:\n%s\n", detail.LastResult)
	}
}

func doCommand(id, prompt string) {
	body, _ := json.Marshal(map[string]string{"text": prompt})

	resp, err := http.Post(
		baseURL()+"/ctl/workers/"+id+"/command",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintf(os.Stderr, "Worker %s not found.\n", id)
		os.Exit(1)
	}

	fmt.Printf("Command sent to worker %s.\n", id)
}

func doWait(id string) {
	// Poll worker status until idle or error.
	for {
		resp, err := http.Get(baseURL() + "/ctl/workers/" + id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		var detail struct {
			Status     string `json:"status"`
			LastResult string `json:"last_result"`
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			fmt.Fprintf(os.Stderr, "Worker %s not found.\n", id)
			os.Exit(1)
		}

		json.Unmarshal(data, &detail)

		if detail.Status == "idle" || detail.Status == "error" || detail.Status == "stopped" {
			if detail.LastResult != "" {
				fmt.Println(detail.LastResult)
			} else {
				fmt.Println("Worker finished (no result text).")
			}
			return
		}

		time.Sleep(2 * time.Second)
	}
}

func doKill(id string) {
	req, _ := http.NewRequest(http.MethodDelete, baseURL()+"/ctl/workers/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintf(os.Stderr, "Worker %s not found.\n", id)
		os.Exit(1)
	}

	fmt.Printf("Worker %s killed.\n", id)
}
