package main

import (
	"fmt"
	"log"

	"github.com/lxc/incus/v7/client"
)

func main() {
	c, err := incus.ConnectIncusUnix("/var/lib/incus/unix.socket", nil)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}

	networks, err := c.GetNetworks()
	if err != nil {
		log.Fatalf("Failed to get networks: %v", err)
	}

	for _, net := range networks {
		fmt.Printf("Network: %s, Type: %s, Config: %v\n", net.Name, net.Type, net.Config)
	}
}
