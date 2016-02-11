# avr-structparser
MQTT based parser for decoding received JeeNode data into corresponding values. All configuration is received from MQTT topics.

## Current state
The current version should work and happily decode signed and unsigned integers and bool values from the received data. The resulting values are logged to console but not yet published to MQTT.

It probably needs some minor modifications to acceppt *standard* JeeNode log messages as the messages at my place do have an additional field showing the signal level during reception.


## Principle of Operation
`avr-structparser` reads all received messages on `logger/jeelink/…` and tries to parse them. This will succeed, if it's one of the classic JeeNode messages like these:

````
OK 5 1 102 221 120 1 (6)
OK 25 1 104 217 92 3 (6)
OK 6 210 148 220 5 (6)
OK 26 1 106 215 48 3 (6)
OK 5 1 102 220 120 1 (6)
````

Next step is to extract the `nodeId` from the first data byte. This number is used to look up some informations about this specific node like *location* and *driver*. *location* is a short string descriping the *location* of this node. This string will be used to publish the decoded values to MQTT. *driver* is also a string describing the data contained in the remaining received bytes. A configuration for **node #5** is stored in MQTT at `config/tht/avr-structparser/nodes/5` and looks like this:

````json
{
  "driver": "roomnode.1",
  "location":"entrance",
  "flags": [ "pir" ]
}
````

> Please note: The configuration stored in MQTT has to be on one single line. Newlines are not allowed and only included here to improve readability.

The next step for `avr-structparser` is to look up the data format used by this node. This configuration is also stored in MQTT at `config/tht/avr-structparser/drivers/roomnode.1` and looks like this:

````json
[
  { "size":8, "fieldType":"uint", "label":"light", "multi":1, "round":-1, "unit":"", "retain":true, "ignoreUnless":"" },
  { "size":1, "fieldType":"bool", "label":"moved", "multi":1, "round":-1, "unit":"", "retain":true, "ignoreUnless":"pir" },
  { "size":7, "fieldType":"uint", "label":"humidity", "multi":1, "round":-1, "unit":"%", "retain":true, "ignoreUnless":"" },
  { "size":10, "fieldType":"int", "label":"temp", "multi":0.1, "round":1, "unit":"°C", "retain":true, "ignoreUnless":"" },
  { "size":8, "fieldType":"batvolt", "label":"bat", "multi":1, "round":-1, "unit":"V", "retain":true, "ignoreUnless":"" }
]
````

> Please note: The configuration stored in MQTT has to be on one single line. Newlines are not allowed and only included here to improve readability.

This data actually describes the *struct* used to pack the values on the JeeNode. This is how the *struct* is used inside the *Sketch*:

````c
struct {
    byte light;     // light sensor: 0..255
    byte moved :1;  // motion detector: 0..1
    byte humi  :7;  // humidity: 0..100
    int temp   :10; // temperature: -500..+500 (tenths)
    unsigned int bat:8;  // battery-level
} payload;
````

Order, size and datatype have to match for the decoding to succeed. *label* is used when publishing the resulting value to MQTT. *multi* and *round* are only used on *int* datatypes and allow for some scaling and rounding of the resulting numbers. *Temperature* is submitted as a 10bit signed integer in 1/10°C. The above configuration (`"multi":0.1, "round":1`) make sure the final result is in °C with one number after the decimal point.

I highly recommend to use `jet pub -r …` to permanently push the configuration to MQTT. `avr-structparser` does not store the configuration when shut down so it has to be able to receive a complete set of configuration from MQTT while starting up.

As soon as all the values are extracted they are published to MQTT (not yet implemented!).
