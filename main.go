package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	mqtt "git.eclipse.org/gitroot/paho/org.eclipse.paho.mqtt.golang.git"
	"log"
	"math"
	"os"
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

	mqtt := os.Getenv("HUB_MQTT")
	if mqtt == "" {
		log.Println("Using fallback MQTT URI as environment 'HUB_MQTT' is not set.")
		mqtt = "tcp://127.0.0.1:1883"
	}
	parserStatus := connectToHub("avr-structparser", mqtt, true)
	defer hub.Disconnect(250)

	go nodeConfigListener("config/tht/avr-structparser/nodes/+")
	go driverConfigListener("config/tht/avr-structparser/drivers/+")

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
	options.SetBinaryWill(clientName+"/"+clientID, nil, 1, retain)
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
	feed := make(chan event, 100)

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
			if 0 == uint_onenumber&(1<<j) {
				data[8*i+int(j)] = '0'
			} else {
				data[8*i+int(j)] = '1'
			}
		}
	}

	return data
}

/**
 * nodeConfigListener
 * Listens for node configurations and updates internal data structure
 */
func nodeConfigListener(feed string) {
	for evt := range topicWatcher(feed) {

		// Output received data
		log.Printf("Receiving configuration: %s", string(evt.Topic))

		// Extract nodeId
		var nodeId uint
		if number, err := strconv.ParseUint(evt.Topic[strings.LastIndex(evt.Topic, "/")+1:], 10, 64); err == nil {
			nodeId = uint(number)
		} else {
			log.Println("Unable to parse nodeId from topic, plese check configuration")
			continue
		}

		// Now check payload. If too short it CAN'T be a valid configuration - purge data from internal configuration
		if len(evt.Payload) < 4 {
			delete(nodes, nodeId)

		} else {
			// Payload not empty. Parse JSON
			var payload nodeInfo
			if err := json.Unmarshal(evt.Payload, &payload); err != nil {
				log.Println("Error while trying to parse JSON", err)
				return
			}
			log.Printf("Configuration received for node %d (%s) using '%s'", nodeId, payload.Location, payload.Driver)
			nodes[nodeId] = payload
		}
	}
}

/**
 * driverConfigListener
 * Listens for driver configurations and updates internal data structure
 */
func driverConfigListener(feed string) {
	for evt := range topicWatcher(feed) {

		// Output received data
		log.Printf("Receiving configuration: %s", string(evt.Topic))

		// Extract driver name
		driver := string(evt.Topic[strings.LastIndex(evt.Topic, "/")+1:])

		// Now check payload. If too short it CAN'T be a valid configuration - purge data from internal configuration
		if len(evt.Payload) < 4 {
			delete(drivers, driver)

		} else {
			// Payload not empty. Parse JSON
			var payload []fieldTemplate
			if err := json.Unmarshal(evt.Payload, &payload); err != nil {
				log.Println("Error while trying to parse JSON", err)
				return
			}
			log.Printf("Decoded data for driver '%s' using %d fields", driver, len(payload))
			drivers[driver] = payload
		}
	}
}

/**
 * logListener
 * Listens for log messages and parses received data.
 */
func logListener(feed string) {
	channel := topicWatcher(feed)

	// wait for configuration to arrive and start doing work afterwards
	time.Sleep(2000 * time.Millisecond)
	log.Println("Done waiting for configuration, start handling REAL data...")

	for evt := range channel {

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

		// check if we need to ignore last part (signal level on my implementation)
		// TODO: Find a way to include this additional information in MQTT pub
		tailSkip := 0
		if slices[len(slices)-1][0] == '(' {
			tailSkip = 1
		}

		// decode received data
		bin := toBinData(slices[2 : len(slices)-tailSkip])
		log.Printf("=> %v", string(bin))
		decode(uint(origin), ts, bin)
	}
}

// struct defining one single parsable data field
type fieldTemplate struct {
	Size             uint8
	FieldType, Label string
	Multi            float32
	Round            int
	Unit             string
	Retain           bool
	IgnoreUnless     string // ignore this field unless node has corresponding flag set
}

// Correlation between sketches (driver) and fields contained in struct (data received on MQTT)
var drivers = map[string][]fieldTemplate{}

/*
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
*/

// Correlation between nodeIds and sketch running on it (data received on MQTT)
type nodeInfo struct {
	Driver, Location string
	Flags            []string
}

var nodes = map[uint]nodeInfo{}

/*
	5:  {"roomnode.1", "entrance", []string{"pir"}},
	6:  {"doornode.1", "north", []string{"door1", "door2", "door3"}},
	7:  {"roomnode.1", "outside", []string{}},
	25: {"roomnode.1", "office", []string{}},
	26: {"roomnode.1", "shower", []string{}},
*/

/**
 * identifyNode
 * Returns nodeInfo and []fieldTemplate describing how to parse data for supplied nodeId.
 * Will return an error if node is unknown.
 */
func identifyNode(origin uint) (nodeInfo, []fieldTemplate, error) {
	node := nodes[origin]
	if node.Location == "" {
		return nodeInfo{}, nil, fmt.Errorf("Node '%d' is not known, ignoring data", origin)
	}
	pattern := drivers[node.Driver]
	if len(pattern) == 0 {
		return nodeInfo{}, nil, fmt.Errorf("Node '%d' uses unknown driver '%s', ignoring data", origin, node.Driver)
	}
	return node, pattern, nil
}

/**
 * decode
 * Decodes data and publishes results to MQTT
 */
func decode(origin uint, ts uint64, data []byte) {
	// locate configuration for node
	node, pattern, err := identifyNode(origin)
	if err != nil {
		log.Println(err)
		return
	}
	log.Printf("=> Node %d (%s) is using driver %s", origin, node.Location, node.Driver)

	results := make(map[string]interface{})
	var start uint = 0
	for _, field := range pattern {
		// Extracting bits for this specific field and rotate them once more while doing so
		part := make([]byte, field.Size)
		for j := uint(0); j < uint(field.Size); j++ {
			part[j] = data[start+uint(field.Size)-1-j]
		}

		// increment start-index for next field
		start = start + uint(field.Size)

		// handle ignoreUnless and check if flag is present
		if field.IgnoreUnless != "" {
			skip := true
			for _, flag := range node.Flags {
				if flag == field.IgnoreUnless {
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
			"unit":    field.Unit,
		}

		switch fieldType := field.FieldType; fieldType {
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
		results[field.Label] = result

	}
	log.Printf("%v", results)
}

/**
 * handleMultiplicator
 * Applies "multi" and "round" to value if needed. This is applied to all integer
 * types.
 */
func handleMultiplicator(field fieldTemplate, data []byte, value int) interface{} {
	if field.Multi != 1.0 {
		floatValue := float64(field.Multi) * float64(value)
		shift := math.Pow(10, float64(field.Round))
		floatValue = math.Floor((shift*floatValue)+0.5) / shift
		log.Printf("=> Decoded '%s' (%s, %s) as: %s%s", string(data),
			field.Label, field.FieldType, strconv.FormatFloat(floatValue, 'f',
				field.Round, 64), field.Unit)
		return floatValue
	} else {
		log.Printf("=> Decoded '%s' (%s, %s) as: %d%s", string(data), field.Label,
			field.FieldType, value, field.Unit)
		return value
	}
}

/**
 * decodeUint
 * Decodes an unsigned integer. It may return a int or float if there is a multiplicator
 * on the field.
 */
func decodeUint(field fieldTemplate, data []byte) interface{} {
	value, _ := strconv.ParseInt(string(data), 2, 32)

	return handleMultiplicator(field, data, int(value))
}

/**
 * decodeInt
 * Decodes a signed integer. It may return a int or float if there is a multiplicator
 * on the field.
 */
func decodeInt(field fieldTemplate, data []byte) interface{} {
	unsig_tmp, _ := strconv.ParseInt(string(data), 2, 32)

	unsig := int(unsig_tmp)
	mask := 1 << (field.Size - 1)
	value := (unsig & ^mask) - (unsig & mask)

	return handleMultiplicator(field, data, value)
}

/**
 * decodeBool
 * Decodes a boolean value, returns true or false
 */
func decodeBool(field fieldTemplate, data []byte) bool {
	value := (data[0] == 49)
	log.Printf("-> Decoded '%s' (%s, %s) as: %t%s", string(data), field.Label,
		field.FieldType, value, field.Unit)
	return value
}

/**
 * decodeBatvolt
 * Decodes packed battery voltage. Use the following Arduino (C) code and set it as
 * an uint8_t: bat = (analogRead(A5)-100)>>2;
 */
func decodeBatvolt(field fieldTemplate, data []byte) float64 {
	intValue, _ := strconv.ParseInt(string(data), 2, 64)
	value := float64(3.3/1024) * float64(intValue*4+100)
	value = math.Floor((100*value)+0.5) / 100
	log.Printf("-> Decoded '%s' (%s, %s) as: %s%s", string(data), field.Label,
		field.FieldType, strconv.FormatFloat(value, 'f', 1, 64), field.Unit)
	return value
}
