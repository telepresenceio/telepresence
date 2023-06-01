package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/datawire/dlib/dlog"
	"github.com/sirupsen/logrus"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

func main() {
	cfg := client.GetDefaultConfig()
	bCtx := client.WithConfig(context.Background(), &cfg)
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	bCtx = dlog.WithLogger(bCtx, dlog.WrapLogrus(logger))
	vif.InitLogger(bCtx)

	ctx, cancel := context.WithCancel(client.WithConfig(bCtx, &cfg))
	defer cancel()
	dev, err := vif.NewTunnelingDevice(ctx, func(context.Context, tunnel.ConnID) (tunnel.Stream, error) {
		return nil, errors.New("stream routing not enabled; refusing to forward")
	})
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := dev.Close(bCtx); err != nil {
			panic(err)
		}
	}()
	go func() {
		err := dev.Run(ctx)
		if err != nil {
			panic(err)
		}
	}()
	yesRoutes := []*net.IPNet{}
	noRoutes := []*net.IPNet{}
	whitelist := []*net.IPNet{}
	for _, cidr := range os.Args[1:] {
		var ipnet *net.IPNet
		var err error
		if strings.HasPrefix(cidr, "!") {
			_, ipnet, err = net.ParseCIDR(strings.TrimPrefix(cidr, "!"))
			fmt.Printf("Blacklisting route: %s\n", ipnet)
			noRoutes = append(noRoutes, ipnet)
		} else if strings.HasPrefix(cidr, "+") {
			_, ipnet, err = net.ParseCIDR(strings.TrimPrefix(cidr, "+"))
			fmt.Printf("Whitelisting route: %s\n", ipnet)
			whitelist = append(whitelist, ipnet)
		} else {
			_, ipnet, err = net.ParseCIDR(cidr)
			fmt.Printf("Adding route: %s\n", ipnet)
			yesRoutes = append(yesRoutes, ipnet)
		}
		if err != nil {
			panic(err)
		}
	}
	dev.Router.UpdateWhitelist(whitelist)
	err = dev.Router.UpdateRoutes(ctx, yesRoutes, noRoutes)
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			scanner := bufio.NewScanner(os.Stdin)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.TrimSpace(line) == "" {
					cancel()
					return
				}
			}
			if err := scanner.Err(); err != nil {
				panic(err)
			}
		}
	}()
	defer fmt.Println("Okay bye!")
	fmt.Printf("Device: %s\n", dev.Device.Name())
	fmt.Println("Press enter (empty line) to end")
	<-ctx.Done()
}
