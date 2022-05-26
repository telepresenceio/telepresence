package integration_test

import (
	"fmt"
	"net"
	"strings"
	"time"
)

func (s *connectedSuite) TestUDPEcho() {
	ctx := s.Context()
	require := s.Require()
	svc := "udp-echo"
	tag := "docker.io/thhal/udp-test:0.1.0"

	require.NoError(s.Kubectl(ctx, "create", "deploy", svc, "--image", tag))
	require.NoError(s.Kubectl(ctx, "expose", "deploy", svc, "--port", "80", "--protocol", "UDP", "--target-port", "8080"))
	defer func() {
		_ = s.Kubectl(ctx, "delete", "svc,deploy", svc)
	}()
	require.NoError(s.RolloutStatusWait(ctx, "deploy/"+svc))

	var conn net.Conn
	require.Eventually(
		func() bool {
			var err error
			conn, err = net.Dial("udp", fmt.Sprintf("%s.%s:80", svc, s.AppNamespace()))
			return err == nil
		},
		12*time.Second, // waitFor
		3*time.Second,  // polling interval
		`dial never succeeds`)

	defer conn.Close()

	mb := strings.Builder{}
	mb.WriteString("This is ")
	for i := 0; i < 1000; i++ {
		mb.WriteString("a russian doll containing ")
	}
	mb.WriteString("a solid russian doll")

	buf := [0x10000]byte{}

	echoTest := func(msg string) {
		_, err := conn.Write([]byte(msg))
		require.NoError(err)
		require.NoError(conn.SetReadDeadline(time.Now().Add(5 * time.Second)))
		n, err := conn.Read(buf[:])
		require.NoError(err)
		rp := "Reply from UDP-echo: "
		pl := len(rp)
		require.Equal(string(buf[:pl]), rp)
		require.Equal(len(msg)+pl, n)
		require.Equal(msg, string(buf[pl:n]))
	}
	echoTest("Hello")
	echoTest(mb.String())
}
