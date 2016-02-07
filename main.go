package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	mqtt "git.eclipse.org/gitroot/paho/org.eclipse.paho.mqtt.golang.git"
	"log"
	"math"
	"strconv"
	"strings"
	"time"
)

var (
	hub *mqtt.Client
)

type event struct {
	Topic    string
	Payload  []byte
	Retained bool
}

func main() {
	log.Println("Connecting to MQTT... ")

	parserStatus := connectToHub("avr-structparser", "tcp://192.168.42.15:1883", true)
	//parserStatus := connectToHub("avr-structparser", "tcp://localhost:1883", true)
	defer hub.Disconnect(250)

	go logListener("logger/jeelink/+")

	parserStatus <- 1

	done := make(chan struct{})
	<-done // hang around forever
}

// connectToHub sets up an MQTT client and registers as a "jet/..." client.
// Uses last-will to automatically unregister on disconnect. This returns a
// "topic notifier" channel to allow updating the registered status value.
func connectToHub(clientName, port string, retain bool) chan<- interface{} {
	// add a "fairly random" 6-digit suffix to make the client name unique
	nanos := time.Now().UnixNano()
	clientID := fmt.Sprintf("%s/%06d", clientName, nanos%1e6)

	options := mqtt.NewClientOptions()
	options.AddBroker(port)
	options.SetClientID(clientID)
	options.SetKeepAlive(10)
	options.SetBinaryWill("jet/"+clientID, nil, 1, retain)
	hub = mqtt.NewClient(options)

	if t := hub.Connect(); t.Wait() && t.Error() != nil {
		log.Fatal(t.Error())
	}

	if retain {
		log.Println("connected as", clientID, "to", port)
	}

	// register as jet client, cleared on disconnect by the will
	feed := topicNotifier("jet/"+clientID, retain)
	feed <- 0 // start off with state "0" to indicate connection

	// return a topic feed to allow publishing hub status changes
	return feed
}

// topicWatcher turns an MQTT subscription into a channel feed of events.
func topicWatcher(pattern string) <-chan event {
	feed := make(chan event)

	t := hub.Subscribe(pattern, 0, func(hub *mqtt.Client, msg mqtt.Message) {
		feed <- event{
			Topic:    msg.Topic(),
			Payload:  msg.Payload(),
			Retained: msg.Retained(),
		}
	})
	if t.Wait() && t.Error() != nil {
		log.Fatal(t.Error())
	}

	return feed
}

// topicNotifier returns a channel which publishes all its messages to MQTT.
func topicNotifier(topic string, retain bool) chan<- interface{} {
	feed := make(chan interface{})

	go func() {
		for msg := range feed {
			sendToHub(topic, msg, retain)
		}
	}()

	return feed
}

// sendToHub publishes a message, and waits for it to complete successfully.
// Note: does no JSON conversion if the payload is already a []byte.
func sendToHub(topic string, payload interface{}, retain bool) {
	data, ok := payload.([]byte)
	if !ok {
		var e error
		data, e = json.Marshal(payload)
		if e != nil {
			log.Println("json conversion failed:", e, payload)
			return
		}
	}
	t := hub.Publish(topic, 1, retain, data)
	if t.Wait() && t.Error() != nil {
		log.Print(t.Error())
	}
}

/**
 * toBinString
 * Convert received array of bytes representing one byte each to a byte array representing the
 * binary value of the reverted bytes (LSB<>MSB). The returned bytes are '0' and '1'
 */
func toBinData(slices [][]byte) []byte {
	// get the bytes out of the string
	data := make([]byte, 8*(len(slices)))

	// ignore first two and last part (OK, Header and signal quality)
	for i, onenumber := range slices {
		// convert string representation (DEC) of number to a binary one
		uint_onenumber, _ := strconv.ParseUint(string(onenumber), 10, 8)

		// Convert byte to binary represantation AND do MSB<>LSB conversation
		for j := uint(0); j < 8; j++ {
			if 0 != uint_onenumber&(1<<j) {
				data[8*i+int(j)] = '1'
			} else {
				data[8*i+int(j)] = '0'
			}
		}
	}

	return data
}

/**
 * logListener
 * Listens for log messages and parses received data.
 */
func logListener(feed string) {
	for evt := range topicWatcher(feed) {

		// ignore line if it does not start with "OK"
		if !bytes.HasPrefix(evt.Payload, []byte("OK ")) {
			continue
		}

		// Parse timestamp as uint64
		ts, _ := strconv.ParseUint(evt.Topic[strings.LastIndex(evt.Topic, "/")+1:], 10, 64)

		// slicing up the data and extract nodeId of source
		slices := bytes.Split(evt.Payload, []byte(" "))
		origin, _ := strconv.ParseUint(string(slices[1]), 10, 8)
		origin = origin & 0x1F

		// Output received data
		log.Printf("received data on %s from %d at time %d", string(evt.Topic), origin, ts)
		log.Printf("=> parsing payload '%s'", string(evt.Payload))

		// decode received data
		bin := toBinData(slices[2 : len(slices)-1])
		log.Printf("=> %v", string(bin))
		decode(uint(origin), ts, bin)
	}
}

// struct defining one single parsable data field
type fieldTemplate struct {
	size             uint8
	fieldType, label string
	multi            float32
	round            int
	unit             string
	retain           bool
	ignoreUnless     string // ignore this field unless node has corresponding flag set
}

// Correlation between sketches (driver) and fields contained in struct
var drivers = map[string][]fieldTemplate{
	"roomnode.1": {
		{8, "uint", "light", 1, -1, "", true, ""},
		{1, "bool", "moved", 1, -1, "", true, "pir"},
		{7, "uint", "humidity", 1, -1, "%", true, ""},
		{10, "int", "temp", 0.1, 1, "°C", true, ""},
		{8, "batvolt", "bat", 1, -1, "V", true, ""},
	},
	"doornode.1": {
		{10, "int", "temp", 0.1, 1, "°C", true, ""},
		{19, "uint", "pressure", 0.01, 0, "", true, ""},
		{1, "bool", "door1", 1, -1, "", true, "door1"},
		{1, "bool", "door2", 1, -1, "", true, "door2"},
		{1, "bool", "door3", 1, -1, "", true, "door3"},
	},
}

// Correlation between nodeIds and sketch running on it
var nodes = map[uint]struct {
	driver, location string
	flags            []string
}{
	5:  {"roomnode.1", "entrance", []string{"pir"}},
	6:  {"doornode.1", "north", []string{"door1", "door2", "door3"}},
	7:  {"roomnode.1", "outside", []string{}},
	25: {"roomnode.1", "office", []string{}},
	26: {"roomnode.1", "shower", []string{}},
}

func decode(origin uint, ts uint64, data []byte) {
	// locate configuration for node
	node := nodes[origin]
	if node.location == "" {
		log.Printf("!! Node '%d' is not known, ignoring data", origin)
		return
	}
	pattern := drivers[node.driver]
	if len(pattern) == 0 {
		log.Printf("!! Node '%d' uses unknown driver '%s', ignoring data", origin, node.driver)
		return
	}
	log.Printf("=> Node %d (%s) is using driver %s", origin, node.location, node.driver)

	results := make(map[string]interface{})
	var start uint = 0
	for _, field := range pattern {
		// Extracting bits for this specific field and rotate them once more while doing so
		part := make([]byte, field.size)
		for j := uint(0); j < uint(field.size); j++ {
			part[j] = data[start+uint(field.size)-1-j]
		}

		// increment start-index for next field
		start = start + uint(field.size)

		// handle ignoreUnless and check if flag is present
		if field.ignoreUnless != "" {
			skip := true
			for _, flag := range node.flags {
				if flag == field.ignoreUnless {
					skip = false
				}
			}
			if skip {
				continue
			}
		}

		// prepare result for this item
		result := map[string]interface{}{
			"updated": ts,
			"unit":    field.unit,
		}

		switch fieldType := field.fieldType; fieldType {
		case "uint":
			result["value"] = decodeUint(field, part)
		case "int":
			result["value"] = decodeInt(field, part)
		case "bool":
			result["value"] = decodeBool(field, part)
		case "batvolt":
			result["value"] = decodeBatvolt(field, part)
		case "ignore":
			continue
		default:
			log.Printf("Don't know how to handle '%s'", fieldType)
		}
		results[field.label] = result

	}
	log.Printf("%v", results)
}

func decodeUint(field fieldTemplate, data []byte) uint {
	value, _ := strconv.ParseInt(string(data), 2, 32)
	log.Printf("-> Decoded '%s' (%s, %s) as: %d%s", string(data), field.label, field.fieldType, value, field.unit)
	return uint(value)
}

func decodeInt(field fieldTemplate, data []byte) interface{} {
	unsig_tmp, _ := strconv.ParseInt(string(data), 2, 32)

	unsig := int(unsig_tmp)
	mask := 1 << (field.size - 1)
	value := (unsig & ^mask) - (unsig & mask)
	if field.multi != 1.0 {
		floatValue := float64(field.multi) * float64(value)
		shift := math.Pow(10, float64(field.round))
		floatValue = math.Floor((shift*floatValue)+0.5) / shift
		log.Printf("-> Decoded '%s' (%s, %s) as: %s%s", string(data),
			field.label, field.fieldType, strconv.FormatFloat(floatValue, 'f',
				field.round, 64), field.unit)
		return floatValue
	} else {
		log.Printf("-> Decoded '%s' (%s, %s) as: %d%s", string(data), field.label,
			field.fieldType, value, field.unit)
		return value
	}
}

func decodeBool(field fieldTemplate, data []byte) bool {
	value := (data[0] == 49)
	log.Printf("-> Decoded '%s' (%s, %s) as: %t%s", string(data), field.label,
		field.fieldType, value, field.unit)
	return value
}

func decodeBatvolt(field fieldTemplate, data []byte) float64 {
	intValue, _ := strconv.ParseInt(string(data), 2, 64)
	value := float64(3.3/1024) * float64(intValue*4+100)
	log.Printf("-> Decoded '%s' (%s, %s) as: %s%s", string(data), field.label,
		field.fieldType, strconv.FormatFloat(value, 'f', 1, 64), field.unit)
	return value
}
