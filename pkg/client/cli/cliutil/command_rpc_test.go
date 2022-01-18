package cliutil

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func commas(s string) string {
	return strings.Trim(strings.Join(strings.Fields(s), ","), "[]")
}

func TestToFromRPC(t *testing.T) { //nolint:gocognit,gocyclo // it's a long list
	// I apologize in advance if you're reading this test; it's very repetitive, but I couldn't figure out
	// a type-safe way to make this all generic
	rCmd := &cobra.Command{
		Use:  "test-command",
		RunE: func(_ *cobra.Command, _ []string) error { return nil },
	}
	var (
		valBool                   = true
		valBoolSlice              = []bool{false, false, true}
		valBytesBase64            = []byte{0xDE, 0xAD}
		valBytesHex               = []byte{0xBE, 0xEF}
		valDuration               = 30 * time.Minute
		valDurationSlice          = []time.Duration{32 * time.Second, 45 * time.Hour}
		valFloat32        float32 = 1.8
		valFloat32Slice           = []float32{3.4, 5.7}
		valFloat64        float64 = 9.7
		valFloat64Slice           = []float64{9.3, 33.98}
		valIP                     = net.ParseIP("192.168.1.1")
		valIPMask                 = net.IPv4Mask(255, 255, 0, 0)
		valIPNet                  = net.IPNet{IP: net.ParseIP("8.8.0.0"), Mask: net.IPv4Mask(255, 255, 0, 0)}
		valIPSlice                = []net.IP{net.ParseIP("192.3.1.30"), net.ParseIP("192.3.77.111")}
		valInt            int     = 343
		valInt16          int16   = -3331
		valInt32          int32   = 433
		valInt32Slice             = []int32{-97, 22, 59}
		valInt64          int64   = 442
		valInt64Slice             = []int64{78, -3, 50000}
		valInt8           int8    = 9
		valIntSlice               = []int{76, 33}
		valString                 = "giovanni boccaccio"
		valStringArray            = []string{"hey", "you"}
		valStringSlice            = []string{"one", "two", "three"}
		valStringToInt            = map[string]int{"blah": 45}
		valStringToInt64          = map[string]int64{"another": 77}
		valStringToString         = map[string]string{"foo": "bar"}
		valUint           uint    = 17
		valUint16         uint16  = 123
		valUint32         uint32  = 987
		valUint64         uint64  = 767
		valUint8          uint8   = 33
		valUintSlice              = []uint{44, 999, 666}
	)

	rCmd.Flags().Bool("bool", false, "")
	rCmd.Flags().BoolSlice("bool-slice", []bool{}, "")
	rCmd.Flags().BytesBase64("bytes-base64", []byte{}, "")
	rCmd.Flags().BytesHex("bytes-hex", []byte{}, "")
	rCmd.Flags().Count("count", "")
	rCmd.Flags().Duration("duration", time.Second, "")
	rCmd.Flags().DurationSlice("duration-slice", []time.Duration{}, "")
	rCmd.Flags().Float32("float32", 0, "")
	rCmd.Flags().Float32Slice("float32-slice", []float32{}, "")
	rCmd.Flags().Float64("float64", 0, "")
	rCmd.Flags().Float64Slice("float64-slice", []float64{}, "")
	rCmd.Flags().IP("ip", net.IP{}, "")
	rCmd.Flags().IPMask("ip-mask", net.IPMask{}, "")
	rCmd.Flags().IPNet("ipnet", net.IPNet{}, "")
	rCmd.Flags().IPSlice("ip-slice", []net.IP{}, "")
	rCmd.Flags().Int("int", 0, "")
	rCmd.Flags().Int16("int16", 0, "")
	rCmd.Flags().Int32("int32", 0, "")
	rCmd.Flags().Int32Slice("int32-slice", []int32{}, "")
	rCmd.Flags().Int64("int64", 0, "")
	rCmd.Flags().Int64Slice("int64-slice", []int64{}, "")
	rCmd.Flags().Int8("int8", 0, "")
	rCmd.Flags().IntSlice("int-slice", []int{}, "")
	rCmd.Flags().String("string", "", "")
	rCmd.Flags().StringArray("string-array", []string{}, "")
	rCmd.Flags().StringSlice("string-slice", []string{}, "")
	rCmd.Flags().StringToInt("string-to-int", map[string]int{}, "")
	rCmd.Flags().StringToInt64("string-to-int64", map[string]int64{}, "")
	rCmd.Flags().StringToString("string-to-string", map[string]string{}, "")
	rCmd.Flags().Uint("uint", 0, "")
	rCmd.Flags().Uint16("uint16", 0, "")
	rCmd.Flags().Uint32("uint32", 0, "")
	rCmd.Flags().Uint64("uint64", 0, "")
	rCmd.Flags().Uint8("uint8", 0, "")
	rCmd.Flags().UintSlice("uint-slice", []uint{}, "")

	rpc := CommandsToRPC(CommandGroups{"Test": []*cobra.Command{rCmd}})
	grp, err := RPCToCommands(rpc, func(cmd *cobra.Command, _ []string) error {
		cmdBool, err := cmd.Flags().GetBool("bool")
		if err != nil {
			t.Error(err)
		}
		cmdBoolSlice, err := cmd.Flags().GetBoolSlice("bool-slice")
		if err != nil {
			t.Error(err)
		}
		cmdBytesBase64, err := cmd.Flags().GetBytesBase64("bytes-base64")
		if err != nil {
			t.Error(err)
		}
		cmdBytesHex, err := cmd.Flags().GetBytesHex("bytes-hex")
		if err != nil {
			t.Error(err)
		}
		cmdCount, err := cmd.Flags().GetCount("count")
		if err != nil {
			t.Error(err)
		}
		cmdDuration, err := cmd.Flags().GetDuration("duration")
		if err != nil {
			t.Error(err)
		}
		cmdDurationSlice, err := cmd.Flags().GetDurationSlice("duration-slice")
		if err != nil {
			t.Error(err)
		}
		cmdFloat32, err := cmd.Flags().GetFloat32("float32")
		if err != nil {
			t.Error(err)
		}
		cmdFloat32Slice, err := cmd.Flags().GetFloat32Slice("float32-slice")
		if err != nil {
			t.Error(err)
		}
		cmdFloat64, err := cmd.Flags().GetFloat64("float64")
		if err != nil {
			t.Error(err)
		}
		cmdFloat64Slice, err := cmd.Flags().GetFloat64Slice("float64-slice")
		if err != nil {
			t.Error(err)
		}
		cmdIP, err := cmd.Flags().GetIP("ip")
		if err != nil {
			t.Error(err)
		}
		cmdIPNet, err := cmd.Flags().GetIPNet("ipnet")
		if err != nil {
			t.Error(err)
		}
		cmdIPSlice, err := cmd.Flags().GetIPSlice("ip-slice")
		if err != nil {
			t.Error(err)
		}
		cmdIPv4Mask, err := cmd.Flags().GetIPv4Mask("ip-mask")
		if err != nil {
			t.Error(err)
		}
		cmdInt, err := cmd.Flags().GetInt("int")
		if err != nil {
			t.Error(err)
		}
		cmdInt16, err := cmd.Flags().GetInt16("int16")
		if err != nil {
			t.Error(err)
		}
		cmdInt32, err := cmd.Flags().GetInt32("int32")
		if err != nil {
			t.Error(err)
		}
		cmdInt32Slice, err := cmd.Flags().GetInt32Slice("int32-slice")
		if err != nil {
			t.Error(err)
		}
		cmdInt64, err := cmd.Flags().GetInt64("int64")
		if err != nil {
			t.Error(err)
		}
		cmdInt64Slice, err := cmd.Flags().GetInt64Slice("int64-slice")
		if err != nil {
			t.Error(err)
		}
		cmdInt8, err := cmd.Flags().GetInt8("int8")
		if err != nil {
			t.Error(err)
		}
		cmdIntSlice, err := cmd.Flags().GetIntSlice("int-slice")
		if err != nil {
			t.Error(err)
		}
		cmdString, err := cmd.Flags().GetString("string")
		if err != nil {
			t.Error(err)
		}
		cmdStringArray, err := cmd.Flags().GetStringArray("string-array")
		if err != nil {
			t.Error(err)
		}
		cmdStringSlice, err := cmd.Flags().GetStringSlice("string-slice")
		if err != nil {
			t.Error(err)
		}
		cmdStringToInt, err := cmd.Flags().GetStringToInt("string-to-int")
		if err != nil {
			t.Error(err)
		}
		cmdStringToInt64, err := cmd.Flags().GetStringToInt64("string-to-int64")
		if err != nil {
			t.Error(err)
		}
		cmdStringToString, err := cmd.Flags().GetStringToString("string-to-string")
		if err != nil {
			t.Error(err)
		}
		cmdUint, err := cmd.Flags().GetUint("uint")
		if err != nil {
			t.Error(err)
		}
		cmdUint16, err := cmd.Flags().GetUint16("uint16")
		if err != nil {
			t.Error(err)
		}
		cmdUint32, err := cmd.Flags().GetUint32("uint32")
		if err != nil {
			t.Error(err)
		}
		cmdUint64, err := cmd.Flags().GetUint64("uint64")
		if err != nil {
			t.Error(err)
		}
		cmdUint8, err := cmd.Flags().GetUint8("uint8")
		if err != nil {
			t.Error(err)
		}
		cmdUintSlice, err := cmd.Flags().GetUintSlice("uint-slice")
		if err != nil {
			t.Error(err)
		}

		if valBool != cmdBool {
			t.Errorf("expected flag %s to be %v was %v", "Bool", valBool, cmdBool)
		}
		if !reflect.DeepEqual(valBoolSlice, cmdBoolSlice) {
			t.Errorf("expected flag %s to be %v was %v", "BoolSlice", valBoolSlice, cmdBoolSlice)
		}
		if !reflect.DeepEqual(valBytesBase64, cmdBytesBase64) {
			t.Errorf("expected flag %s to be %v was %v", "BytesBase64", valBytesBase64, cmdBytesBase64)
		}
		if !reflect.DeepEqual(valBytesHex, cmdBytesHex) {
			t.Errorf("expected flag %s to be %v was %v", "BytesHex", valBytesHex, cmdBytesHex)
		}
		if 3 != cmdCount {
			t.Errorf("expected flag %s to be %v was %v", "Count", 3, cmdCount)
		}
		if !reflect.DeepEqual(valDuration, cmdDuration) {
			t.Errorf("expected flag %s to be %v was %v", "Duration", valDuration, cmdDuration)
		}
		if !reflect.DeepEqual(valDurationSlice, cmdDurationSlice) {
			t.Errorf("expected flag %s to be %v was %v", "DurationSlice", valDurationSlice, cmdDurationSlice)
		}
		if valFloat32 != cmdFloat32 {
			t.Errorf("expected flag %s to be %v was %v", "Float32", valFloat32, cmdFloat32)
		}
		if !reflect.DeepEqual(valFloat32Slice, cmdFloat32Slice) {
			t.Errorf("expected flag %s to be %v was %v", "Float32Slice", valFloat32Slice, cmdFloat32Slice)
		}
		if valFloat64 != cmdFloat64 {
			t.Errorf("expected flag %s to be %v was %v", "Float64", valFloat64, cmdFloat64)
		}
		if !reflect.DeepEqual(valFloat64Slice, cmdFloat64Slice) {
			t.Errorf("expected flag %s to be %v was %v", "Float64Slice", valFloat64Slice, cmdFloat64Slice)
		}
		if !reflect.DeepEqual(valIP, cmdIP) {
			t.Errorf("expected flag %s to be %v was %v", "IP", valIP, cmdIP)
		}
		if !reflect.DeepEqual(valIPMask, cmdIPv4Mask) {
			t.Errorf("expected flag %s to be %v was %v", "IPMask", valIPMask, cmdIPv4Mask)
		}
		if valIPNet.String() != cmdIPNet.String() {
			t.Errorf("expected flag %s to be %v was %v", "IPNet", valIPNet, cmdIPNet)
		}
		if !reflect.DeepEqual(valIPSlice, cmdIPSlice) {
			t.Errorf("expected flag %s to be %v was %v", "IPSlice", valIPSlice, cmdIPSlice)
		}
		if valInt != cmdInt {
			t.Errorf("expected flag %s to be %v was %v", "Int", valInt, cmdInt)
		}
		if valInt16 != cmdInt16 {
			t.Errorf("expected flag %s to be %v was %v", "Int16", valInt16, cmdInt16)
		}
		if valInt32 != cmdInt32 {
			t.Errorf("expected flag %s to be %v was %v", "Int32", valInt32, cmdInt32)
		}
		if !reflect.DeepEqual(valInt32Slice, cmdInt32Slice) {
			t.Errorf("expected flag %s to be %v was %v", "Int32Slice", valInt32Slice, cmdInt32Slice)
		}
		if valInt64 != cmdInt64 {
			t.Errorf("expected flag %s to be %v was %v", "Int64", valInt64, cmdInt64)
		}
		if !reflect.DeepEqual(valInt64Slice, cmdInt64Slice) {
			t.Errorf("expected flag %s to be %v was %v", "Int64Slice", valInt64Slice, cmdInt64Slice)
		}
		if valInt8 != cmdInt8 {
			t.Errorf("expected flag %s to be %v was %v", "Int8", valInt8, cmdInt8)
		}
		if !reflect.DeepEqual(valIntSlice, cmdIntSlice) {
			t.Errorf("expected flag %s to be %v was %v", "IntSlice", valIntSlice, cmdIntSlice)
		}
		if valString != cmdString {
			t.Errorf("expected flag %s to be %v was %v", "String", valString, cmdString)
		}
		if !reflect.DeepEqual(valStringArray, cmdStringArray) {
			t.Errorf("expected flag %s to be %v was %v", "StringArray", valStringArray, cmdStringArray)
		}
		if !reflect.DeepEqual(valStringSlice, cmdStringSlice) {
			t.Errorf("expected flag %s to be %v was %v", "StringSlice", valStringSlice, cmdStringSlice)
		}
		if !reflect.DeepEqual(valStringToInt, cmdStringToInt) {
			t.Errorf("expected flag %s to be %v was %v", "StringToInt", valStringToInt, cmdStringToInt)
		}
		if !reflect.DeepEqual(valStringToInt64, cmdStringToInt64) {
			t.Errorf("expected flag %s to be %v was %v", "StringToInt64", valStringToInt64, cmdStringToInt64)
		}
		if !reflect.DeepEqual(valStringToString, cmdStringToString) {
			t.Errorf("expected flag %s to be %v was %v", "StringToString", valStringToString, cmdStringToString)
		}
		if valUint != cmdUint {
			t.Errorf("expected flag %s to be %v was %v", "Uint", valUint, cmdUint)
		}
		if valUint16 != cmdUint16 {
			t.Errorf("expected flag %s to be %v was %v", "Uint16", valUint16, cmdUint16)
		}
		if valUint32 != cmdUint32 {
			t.Errorf("expected flag %s to be %v was %v", "Uint32", valUint32, cmdUint32)
		}
		if valUint64 != cmdUint64 {
			t.Errorf("expected flag %s to be %v was %v", "Uint64", valUint64, cmdUint64)
		}
		if valUint8 != cmdUint8 {
			t.Errorf("expected flag %s to be %v was %v", "Uint8", valUint8, cmdUint8)
		}
		if !reflect.DeepEqual(valUintSlice, cmdUintSlice) {
			t.Errorf("expected flag %s to be %v was %v", "UintSlice", valUintSlice, cmdUintSlice)
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	var lCmd *cobra.Command

	if cmds, ok := grp["Test"]; ok {
		if len(cmds) != 1 {
			t.Fatalf("unexpected length for command group 'Test'; expected 1 is %d", len(cmds))
		}
		lCmd = cmds[0]
	} else {
		t.Fatalf("group 'Test' not in returned command group: %v", grp)
	}

	if lCmd.Use != rCmd.Use {
		t.Errorf("command use not set properly; expected %s got %s", rCmd.Use, lCmd.Use)
	}
	args := []string{
		fmt.Sprintf("--bool=%t", valBool),
		fmt.Sprintf("--bool-slice=%s", commas(fmt.Sprint(valBoolSlice))),
		fmt.Sprintf("--bytes-base64=%s", base64.StdEncoding.EncodeToString(valBytesBase64)),
		fmt.Sprintf("--bytes-hex=%s", hex.EncodeToString(valBytesHex)),
		"--count", "3",
		fmt.Sprintf("--duration=%s", valDuration),
		fmt.Sprintf("--duration-slice=%s", commas(fmt.Sprint(valDurationSlice))),
		fmt.Sprintf("--float32=%f", valFloat32),
		fmt.Sprintf("--float32-slice=%s", commas(fmt.Sprint(valFloat32Slice))),
		fmt.Sprintf("--float64=%f", valFloat64),
		fmt.Sprintf("--float64-slice=%s", commas(fmt.Sprint(valFloat64Slice))),
		fmt.Sprintf("--ip=%s", valIP),
		fmt.Sprintf("--ipnet=%s", valIPNet.String()),
		fmt.Sprintf("--ip-slice=%s", commas(fmt.Sprint(valIPSlice))),
		fmt.Sprintf("--ip-mask=%s", valIPMask),
		fmt.Sprintf("--int=%d", valInt),
		fmt.Sprintf("--int16=%d", valInt16),
		fmt.Sprintf("--int32=%d", valInt32),
		fmt.Sprintf("--int32-slice=%s", commas(fmt.Sprint(valInt32Slice))),
		fmt.Sprintf("--int64=%d", valInt64),
		fmt.Sprintf("--int64-slice=%s", commas(fmt.Sprint(valInt64Slice))),
		fmt.Sprintf("--int8=%d", valInt8),
		fmt.Sprintf("--int-slice=%s", commas(fmt.Sprint(valIntSlice))),
		fmt.Sprintf("--string=%s", valString),
		"--string-array=" + valStringArray[0], "--string-array=" + valStringArray[1],
		fmt.Sprintf("--string-slice=%s", commas(fmt.Sprint(valStringSlice))),
		fmt.Sprintf("--string-to-int=%s=%d", "blah", valStringToInt["blah"]),
		fmt.Sprintf("--string-to-int64=%s=%d", "another", valStringToInt64["another"]),
		fmt.Sprintf("--string-to-string=%s=%s", "foo", valStringToString["foo"]),
		fmt.Sprintf("--uint=%d", valUint),
		fmt.Sprintf("--uint16=%d", valUint16),
		fmt.Sprintf("--uint32=%d", valUint32),
		fmt.Sprintf("--uint64=%d", valUint64),
		fmt.Sprintf("--uint8=%d", valUint8),
		fmt.Sprintf("--uint-slice=%s", commas(fmt.Sprint(valUintSlice))),
	}
	t.Logf("Running with args %s", strings.Join(args, " "))
	lCmd.SetArgs(args)
	err = lCmd.Execute()
	if err != nil {
		t.Error(err)
	}
}
