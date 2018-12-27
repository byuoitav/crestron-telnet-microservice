package crestrontelnet

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"regexp"
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
func MonitorDMPS(dmps structs.DMPS, killTime time.Time, waitG *sync.WaitGroup) {
	log.L.Debugf("Connecting to %v on %v:23", dmps.Hostname, dmps.Address)

	connection, bufReader, _, err := StartConnection(dmps.Address, "23")

	if err != nil {
		log.L.Warnf("error creating connection. ERROR: %v", err.Error())
		time.Sleep(5 * time.Second)

		if killTime.Before(time.Now()) {
			waitG.Done()
			return
		}

		go MonitorDMPS(dmps, killTime, waitG)
		return
	}

	defer connection.Close()

	for {

		if killTime.Before(time.Now()) {
			waitG.Done()
			return
		}

		connection.SetReadDeadline(time.Now().Add(90 * time.Second))

		response, err := bufReader.ReadString('\n')

		if err != nil {
			log.L.Warnf("Error: [%s]", err)
			log.L.Warnf("Killing and restarting connection")

			go MonitorDMPS(dmps, killTime, waitG)

			return
		}

		match, _ := regexp.MatchString("^~EVENT~", response)

		if match {
			log.L.Debugf("Event Received: %s", response)

			//trim off the leading and ending ~
			response = response[1 : len(response)-1]

			eventParts := strings.Split(response, "~")

			for i := range eventParts {
				eventParts[i] = strings.TrimSpace(eventParts[i])
			}

			log.L.Debugf("Event Parts:%v,  %v", len(eventParts), eventParts)

			if len(eventParts) == 10 {

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

				log.L.Debugf("Sending request to state parser [%v]", x)

				sendResponse, nerr := sendEvent(x)

				if nerr != nil {
					log.L.Warnf("Error sending event %v", nerr.Error())
				} else {
					log.L.Debugf("Sending Event Response: [%s]", sendResponse)
				}

			} else {
				log.L.Warnf("Malformed Event Received: %s", response)
			}

		} else {
			log.L.Debugf("Something else Received: %s", response)
		}
	}
}

func sendEvent(x events.Event) ([]byte, *nerr.E) {
	var reqBody []byte
	var err error

	// marshal request if not already an array of bytes
	reqBody, err = json.Marshal(x)
	if err != nil {
		return []byte{}, nerr.Translate(err)
	}

	// create the request
	address := fmt.Sprintf("%s/legacy/v2/event", eventProcessorHost)
	log.L.Debugf("Sending to address %s", address)

	req, err := http.NewRequest("POST", address, bytes.NewReader(reqBody))
	if err != nil {
		return []byte{}, nerr.Translate(err)
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
		return []byte{}, nerr.Translate(err)
	}
	defer resp.Body.Close()

	// read the resp
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []byte{}, nerr.Translate(err)
	}

	// check resp code
	if resp.StatusCode/100 != 2 {
		msg := fmt.Sprintf("non 200 reponse code received. code: %v, body: %s", resp.StatusCode, respBody)
		return respBody, nerr.Create(msg, http.StatusText(resp.StatusCode))
	}

	return respBody, nil
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
