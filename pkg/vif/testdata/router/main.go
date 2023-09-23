package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

func main() {
	cfg := client.GetDefaultConfig()
	bCtx := client.WithConfig(context.Background(), cfg)
	logger := logrus.StandardLogger()
	logger.SetLevel(logrus.DebugLevel)
	bCtx = dlog.WithLogger(bCtx, dlog.WrapLogrus(logger))
	vif.InitLogger(bCtx)

	ctx, cancel := context.WithCancel(client.WithConfig(bCtx, cfg))

	var err error
	defer func() {
		cancel()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}()

	var dev *vif.TunnelingDevice
	dev, err = vif.NewTunnelingDevice(ctx, func(context.Context, tunnel.ConnID) (tunnel.Stream, error) {
		return nil, errors.New("stream routing not enabled; refusing to forward")
	})
	if err != nil {
		return
	}

	defer func() {
		_ = dev.Close(bCtx)
	}()

	go func() {
		err := dev.Run(ctx)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}()
	yesRoutes := []*net.IPNet{}
	noRoutes := []*net.IPNet{}
	whitelist := []*net.IPNet{}
	for _, cidr := range os.Args[1:] {
		var ipnet *net.IPNet
		if strings.HasPrefix(cidr, "!") {
			if _, ipnet, err = net.ParseCIDR(strings.TrimPrefix(cidr, "!")); err == nil {
				fmt.Printf("Blacklisting route: %s\n", ipnet)
				noRoutes = append(noRoutes, ipnet)
			}
		} else if strings.HasPrefix(cidr, "+") {
			if _, ipnet, err = net.ParseCIDR(strings.TrimPrefix(cidr, "+")); err == nil {
				fmt.Printf("Whitelisting route: %s\n", ipnet)
				whitelist = append(whitelist, ipnet)
			}
		} else {
			if _, ipnet, err = net.ParseCIDR(cidr); err == nil {
				fmt.Printf("Adding route: %s\n", ipnet)
				yesRoutes = append(yesRoutes, ipnet)
			}
		}
		if err != nil {
			return
		}
	}
	dev.Router.UpdateWhitelist(whitelist)
	err = dev.Router.UpdateRoutes(ctx, yesRoutes, noRoutes)
	if err != nil {
		return
	}
	go func() {
		defer cancel()
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				return
			}
		}
		err := scanner.Err()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}()
	defer fmt.Println("Okay bye!")
	fmt.Printf("Device: %s\n", dev.Device.Name())
	fmt.Println("Press enter (empty line) to end")
	<-ctx.Done()
}
