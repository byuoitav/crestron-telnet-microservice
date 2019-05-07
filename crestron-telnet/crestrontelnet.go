package crestrontelnet

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/byuoitav/common/log"
	"github.com/byuoitav/common/nerr"
	"github.com/byuoitav/common/structs"
	"github.com/byuoitav/common/v2/events"
)

var (
	eventProcessorHost   = os.Getenv("EVENT_PROCESSOR_HOST")
	mutex                sync.Mutex
	deviceToSetDebugLogs = make(map[string]bool)
)

//StartMonitoringDevice monitor device - these are simply for monitoring
func StartMonitoringDevice(id string) {
	mutex.Lock()
	defer mutex.Unlock()
	deviceToSetDebugLogs[id] = true
}

//StopMonitoringDevice monitor device
func StopMonitoringDevice(id string) {
	mutex.Lock()
	defer mutex.Unlock()
	deviceToSetDebugLogs[id] = false
}

//IsMonitoringDevice monitor device
func IsMonitoringDevice(id string) bool {
	return false

	// this was slowing it down too much
	// mutex.Lock()
	// defer mutex.Unlock()
	// val, present := deviceToSetDebugLogs[id]
	// if present {
	// 	return val
	// }

	// return false
}

func init() {
	if len(eventProcessorHost) == 0 {
		log.L.Fatalf("EVENT_PROCESSOR_HOST is not set.")
	}
}

//MonitorDMPS is the function to call in a go routine to monitor an individual DMPS
func MonitorDMPS(dmps structs.DMPS, killChannel chan bool, waitG *sync.WaitGroup) {
	if len(dmps.Port) == 0 || dmps.Port == "0" {
		dmps.Port = "23"
	}

	monitor := IsMonitoringDevice(dmps.Hostname)

	if monitor {
		log.L.Warnf("Connecting to %v on %v:%v", dmps.Hostname, dmps.Address, dmps.Port)
	} else {
		log.L.Debugf("Connecting to %v on %v:%v", dmps.Hostname, dmps.Address, dmps.Port)
	}

	connection, bufReader, _, err := StartConnection(dmps.Address, dmps.Port)

	if err != nil {
		log.L.Warnf("error creating connection for %s. ERROR: %v", dmps.Hostname, err.Error())
		time.Sleep(5 * time.Second)

		select {
		case <-killChannel:
			log.L.Debugf("Kill order received for %s", dmps.Hostname)
			waitG.Done()
		default:
			go MonitorDMPS(dmps, killChannel, waitG)
		}

		return
	}

	defer connection.Close()

	for {

		select {
		case <-killChannel:
			log.L.Debugf("Kill order received for %s", dmps.Hostname)
			waitG.Done()
			return
		default:
		}

		monitor = IsMonitoringDevice(dmps.Hostname)

		connection.SetReadDeadline(time.Now().Add(90 * time.Second))

		response, err := bufReader.ReadString('\n')

		if err != nil {
			log.L.Warnf("Error for %s: [%s]", dmps.Hostname, err)
			log.L.Warnf("Killing and restarting connection for %s", dmps.Hostname)

			go MonitorDMPS(dmps, killChannel, waitG)

			return
		}

		match, _ := regexp.MatchString("^~EVENT~", response)

		if !match {
			index := strings.Index(response, "~EVENT~")
			if index > -1 {
				response = response[index:]

				match, _ = regexp.MatchString("^~EVENT~", response)
			}
		}

		if match {
			if monitor {
				log.L.Warnf("Event Received: %s", response)
			} else {
				log.L.Debugf("Event Received: %s", response)
			}

			//trim off the leading and ending ~
			response = strings.TrimSpace(response)
			response = response[1 : len(response)-1]

			eventParts := strings.Split(response, "~")

			for i := range eventParts {
				eventParts[i] = strings.TrimSpace(eventParts[i])
			}

			if monitor {
				log.L.Warnf("Event Parts:%v,  %v", len(eventParts), eventParts)
			} else {
				log.L.Debugf("Event Parts:%v,  %v", len(eventParts), eventParts)
			}

			if len(eventParts) == 9 {

				var x events.Event

				roomParts := strings.Split(eventParts[1], "-")

				for i := range roomParts {
					roomParts[i] = strings.TrimSpace(roomParts[i])
				}

				x.GeneratingSystem = eventParts[1] //hostname

				x.Timestamp, _ = time.Parse(time.RFC3339, eventParts[3]) //timestamp

				x.EventTags = []string{
					strings.Replace(strings.ToLower(eventParts[4]), " ", "-", -1),
					strings.Replace(strings.ToLower(eventParts[5]), " ", "-", -1),
					strings.Replace(strings.ToLower(eventParts[7]), " ", "-", -1)}

				//TargetDevice
				x.TargetDevice = events.BasicDeviceInfo{
					BasicRoomInfo: events.BasicRoomInfo{
						BuildingID: roomParts[0],
						RoomID:     roomParts[0] + "-" + roomParts[1],
					},
					DeviceID: roomParts[0] + "-" + roomParts[1] + "-" + eventParts[6],
				}

				//AffectedRoom
				x.AffectedRoom = events.BasicRoomInfo{
					BuildingID: roomParts[0],
					RoomID:     roomParts[0] + "-" + roomParts[1],
				}

				x.Key = strings.Replace(strings.ToLower(eventParts[7]), " ", "-", -1) //eventKeyInfo

				x.Value = eventParts[8] //eventKeyValue

				x.User = ""

				x.Data = response

				shouldSendEvent := modifyEvent(&x)

				if !shouldSendEvent {
					log.L.Debugf("Ignoring event")
					return
				}

				if monitor {
					log.L.Warnf("Sending request to state parser [%v]", x)
				} else {
					log.L.Debugf("Sending request to state parser [%v]", x)
				}

				nerr := sendEvent(x)

				if nerr != nil {
					log.L.Warnf("Error sending event %v", nerr.Error())
				}

			} else {
				log.L.Warnf("Malformed Event Received: %s", response)
			}

		} else {
			if monitor {
				log.L.Warnf("Something else Received: %s", response)
			} else {
				log.L.Debugf("Something else Received: %s", response)
			}
		}
	}
}

func modifyEvent(event *events.Event) bool {

	//change -CP to -DMPS
	event.GeneratingSystem = strings.Replace(event.GeneratingSystem, "-CP", "-DMPS", -1)
	event.TargetDevice.DeviceID = strings.Replace(event.TargetDevice.DeviceID, "-CP", "-DMPS", -1)

	//hack to fix the items destined for static index
	//that aren't coming in with the right tag
	if event.Key == "software-version" || event.Key == "hardware-version" || event.Key == "volume" || event.Key == "muted" {
		event.AddToTags("core-state")
	}

	if event.Key == "IP Address" {
		event.Key = "ip-address"
		event.AddToTags("core-state")
	}

	if event.Key == "battery-charge-hours-minutes" && event.Value == "Calc" {
		event.Key = "battery-type"
		event.Value = ""
	}

	if event.Key == "battery-charge-hours-minutes" && event.Value == "AA" {
		//convert this to a battery-type router
		event.Key = "battery-type"
		event.Value = "ALKA"
	}

	if event.Key == "responsive" && event.TargetDevice.DeviceID == "BRMB-230-D1" {
		event.Value = "Ok"
	}

	if event.Key == "battery-charge-hours-minutes" && strings.Contains(event.Value, ":") {
		//create another event for battery-charge-minutes
		hm := strings.Split(event.Value, ":")
		h, _ := strconv.Atoi(hm[0])
		m, _ := strconv.Atoi(hm[1])

		minutes := h*60 + m

		newEvent := events.Event{
			GeneratingSystem: event.GeneratingSystem,
			Timestamp:        event.Timestamp,
			EventTags:        event.EventTags,
			TargetDevice:     event.TargetDevice,
			AffectedRoom:     event.AffectedRoom,
			Key:              "battery-charge-minutes",
			Value:            strconv.Itoa(minutes),
			User:             event.User,
			Data:             event.Data,
		}

		nerr := sendEvent(newEvent)

		if nerr != nil {
			log.L.Warnf("Error sending event %v", nerr.Error())
		}

		//also send an battery-type
		newEvent = events.Event{
			GeneratingSystem: event.GeneratingSystem,
			Timestamp:        event.Timestamp,
			EventTags:        event.EventTags,
			TargetDevice:     event.TargetDevice,
			AffectedRoom:     event.AffectedRoom,
			Key:              "battery-type",
			Value:            "",
			User:             event.User,
			Data:             event.Data,
		}

		nerr = sendEvent(newEvent)

		if nerr != nil {
			log.L.Warnf("Error sending event %v", nerr.Error())
		}
	}

	return true
}

func sendEvent(x events.Event) *nerr.E {
	// marshal request if not already an array of bytes
	reqBody, err := json.Marshal(x)
	if err != nil {
		log.L.Debugf("Unable to marshal event %v, error:%v", x, err)
		return nerr.Translate(err)
	}

	eventProcessorHostList := strings.Split(eventProcessorHost, ",")

	for _, hostName := range eventProcessorHostList {

		// create the request
		log.L.Debugf("Sending to address %s", hostName)

		req, err := http.NewRequest("POST", hostName, bytes.NewReader(reqBody))
		if err != nil {
			log.L.Debugf("Unable to post body %v, error:%v", reqBody, err)
			//return []byte{}, nerr.Translate(err)
		}

		//no auth needed for state parser
		//req.SetBasicAuth(username, password)

		// add headers
		req.Header.Add("content-type", "application/json")

		client := http.Client{
			Timeout: 5 * time.Second,
		}

		resp, err := client.Do(req)
		if err != nil {
			log.L.Debugf("error sending request: %v", err)
		} else {
			defer resp.Body.Close()
			// read the resp
			respBody, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				log.L.Debugf("error reading body: %v", err)
				//return []byte{}, nerr.Translate(err)
			} else {

				// check resp code
				if resp.StatusCode/100 != 2 {
					log.L.Debugf("non 200 reponse code received. code: %v, body: %s", resp.StatusCode, respBody)
					//return respBody, nerr.Create(msg, http.StatusText(resp.StatusCode))
				}
			}
		}
	}

	return nil
}

//StartConnection opens connection, performs handshake, waits for first prompt
func StartConnection(address string, port string) (*net.TCPConn, *bufio.Reader, *bufio.Writer, error) {
	tcpAdder, err := net.ResolveTCPAddr("tcp", address+":"+port)
	if err != nil {
		log.L.Debugf("error resolving address. ERROR: %v", err.Error())
		return nil, nil, nil, err
	}

	log.L.Debugf("Resolved to %v", tcpAdder)

	connection, err := net.DialTCP("tcp", nil, tcpAdder)
	if err != nil {
		log.L.Debugf("error connecting to host. ERROR: %v", err.Error())
		return nil, nil, nil, err
	}

	log.L.Debugf("Successfully connected.")

	bufReader := bufio.NewReader(connection)
	bufWriter := bufio.NewWriter(connection)

	bufWriter.WriteString("\r\n")
	bufWriter.Flush()

	connection.SetReadDeadline(time.Now().Add(30 * time.Second))

	response, err := bufReader.ReadString('>')
	if err != nil {
		log.L.Debugf("error reading response. ERROR: %v", err.Error())
		return nil, nil, nil, err
	}

	log.L.Debugf("Initial response %v", response)

	return connection, bufReader, bufWriter, nil
}

//MonitorOtherCrestron is the function to call in a go routine to monitor another crestron device
func MonitorOtherCrestron(otherCrestronDevice structs.DMPS, killChannel chan bool, waitG *sync.WaitGroup) {
	if len(otherCrestronDevice.Port) == 0 || otherCrestronDevice.Port == "0" {
		otherCrestronDevice.Port = "23"
	}

	monitor := IsMonitoringDevice(otherCrestronDevice.Hostname)

	if monitor {
		log.L.Warnf("Connecting to %v on %v:%v", otherCrestronDevice.Hostname, otherCrestronDevice.Address, otherCrestronDevice.Port)
	} else {
		log.L.Debugf("Connecting to %v on %v:%v", otherCrestronDevice.Hostname, otherCrestronDevice.Address, otherCrestronDevice.Port)
	}

	connection, bufReader, bufWriter, err := StartConnection(otherCrestronDevice.Address, otherCrestronDevice.Port)

	if err != nil {
		log.L.Warnf("error creating connection for %s. ERROR: %v", otherCrestronDevice.Hostname, err.Error())
		time.Sleep(5 * time.Second)

		select {
		case <-killChannel:
			log.L.Debugf("Kill order received for %s", otherCrestronDevice.Hostname)
			waitG.Done()
		default:
			go MonitorOtherCrestron(otherCrestronDevice, killChannel, waitG)
		}

		return
	}

	defer connection.Close()

	for {

		monitor = IsMonitoringDevice(otherCrestronDevice.Hostname)

		select {
		case <-killChannel:
			log.L.Debugf("Kill order received for %s", otherCrestronDevice.Hostname)
			waitG.Done()
			return
		default:
		}

		//before we actually write it, flush out the read buffer
		toDiscard := bufReader.Buffered()

		if monitor {
			log.L.Warnf("Clearing out %v that are buffered", toDiscard)
		} else {
			log.L.Debugf("Clearing out %v that are buffered", toDiscard)
		}

		if toDiscard > 0 {
			b1 := make([]byte, toDiscard)
			bufReader.Read(b1)

			if monitor {
				log.L.Warnf("Read and cleared out: %v", b1)
			} else {
				log.L.Debugf("Read and cleared out: %v", b1)
			}
		}

		if len(otherCrestronDevice.CommandToQuery) > 0 {
			log.L.Debugf("Writing %s to %s", otherCrestronDevice.CommandToQuery, otherCrestronDevice.Hostname)
			_, err := bufWriter.WriteString(otherCrestronDevice.CommandToQuery + "\n")
			if err != nil {
				log.L.Warnf("Error for %s while writing: [%s]", otherCrestronDevice.Hostname, err)
			}
		} else {
			log.L.Debugf("Writing %s to %s", "VERSION", otherCrestronDevice.Hostname)
			_, err := bufWriter.WriteString("VERSION\r\n")
			if err != nil {
				log.L.Warnf("Error for %s while writing: [%s]", otherCrestronDevice.Hostname, err)
			}
		}

		err := bufWriter.Flush()

		if err != nil {
			log.L.Warnf("Error for %s when flushing: [%s]", otherCrestronDevice.Hostname, err)
		}

		var response string

		for {
			//wait 30 seconds for response
			connection.SetReadDeadline(time.Now().Add(30 * time.Second))

			response, err = bufReader.ReadString('\n')

			if err != nil {
				log.L.Warnf("Error for %s: [%s]", otherCrestronDevice.Hostname, err)
				log.L.Warnf("Killing and restarting connection for %s", otherCrestronDevice.Hostname)

				go MonitorOtherCrestron(otherCrestronDevice, killChannel, waitG)

				return
			}

			//we got a response, send it as an event
			response = strings.TrimSpace(response)

			if monitor {
				log.L.Warnf("Response for %s received: [%s]", otherCrestronDevice.Hostname, response)
			} else {
				log.L.Debugf("Response for %s received: [%s]", otherCrestronDevice.Hostname, response)
			}

			if len(response) > 0 && response != "VERSION" {
				break
			}
		}

		roomParts := strings.Split(otherCrestronDevice.Hostname, "-")

		for i := range roomParts {
			roomParts[i] = strings.TrimSpace(roomParts[i])
		}

		x := events.Event{
			GeneratingSystem: otherCrestronDevice.Hostname,
			Timestamp:        time.Now(),
			EventTags:        []string{"health", "auto-generated", "heartbeat", "core-state"},
			TargetDevice: events.BasicDeviceInfo{
				BasicRoomInfo: events.BasicRoomInfo{
					BuildingID: roomParts[0],
					RoomID:     roomParts[0] + "-" + roomParts[1],
				},
				DeviceID: otherCrestronDevice.Hostname,
			},
			AffectedRoom: events.BasicRoomInfo{
				BuildingID: roomParts[0],
				RoomID:     roomParts[0] + "-" + roomParts[1],
			},
			Key:   "other-crestron-health-check",
			Value: "response received",
			User:  "",
			Data:  response,
		}

		nerr := sendEvent(x)

		if nerr != nil {
			log.L.Warnf("Error sending event %v", nerr.Error())
		}

		select {
		case <-killChannel:
			log.L.Debugf("Kill order received for %s", otherCrestronDevice.Hostname)
			waitG.Done()
			return
		case <-time.After(30 * time.Second):
			if monitor {
				log.L.Warnf("30 seconds reached for %s", otherCrestronDevice.Hostname)
			} else {
				log.L.Debugf("30 seconds reached for %s", otherCrestronDevice.Hostname)
			}
		}
	}
}
