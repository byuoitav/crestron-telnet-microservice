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
	eventProcessorHost = os.Getenv("EVENT_PROCESSOR_HOST")
)

func init() {
	if len(eventProcessorHost) == 0 {
		log.L.Fatalf("EVENT_PROCESSOR_HOST is not set.")
	}
}

//MonitorDMPS is the function to call in a go routine to monitor an individual DMPS
func MonitorDMPS(dmps structs.DMPS, killChannel chan bool, waitG *sync.WaitGroup) {
	log.L.Debugf("Connecting to %v on %v:23", dmps.Hostname, dmps.Address)

	connection, bufReader, _, err := StartConnection(dmps.Address, "23")

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

		connection.SetReadDeadline(time.Now().Add(90 * time.Second))

		response, err := bufReader.ReadString('\n')

		if err != nil {
			log.L.Warnf("Error for %s: [%s]", dmps.Hostname, err)
			log.L.Warnf("Killing and restarting connection for %s", dmps.Hostname)

			go MonitorDMPS(dmps, killChannel, waitG)

			return
		}

		match, _ := regexp.MatchString("^~EVENT~", response)

		if match {
			log.L.Debugf("Event Received: %s", response)

			//trim off the leading and ending ~
			response = strings.TrimSpace(response)
			response = response[1 : len(response)-1]

			eventParts := strings.Split(response, "~")

			for i := range eventParts {
				eventParts[i] = strings.TrimSpace(eventParts[i])
			}

			log.L.Debugf("Event Parts:%v,  %v", len(eventParts), eventParts)

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

				log.L.Debugf("Sending request to state parser [%v]", x)

				nerr := sendEvent(x)

				if nerr != nil {
					log.L.Warnf("Error sending event %v", nerr.Error())
				}

			} else {
				log.L.Warnf("Malformed Event Received: %s", response)
			}

		} else {
			log.L.Debugf("Something else Received: %s", response)
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

	response, err := bufReader.ReadString('>')
	if err != nil {
		log.L.Debugf("error reading response. ERROR: %v", err.Error())
		return nil, nil, nil, err
	}

	log.L.Debugf("Initial response %v", response)

	return connection, bufReader, bufWriter, nil
}
