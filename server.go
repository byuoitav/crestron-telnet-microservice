package main

import (
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/byuoitav/common"
	"github.com/byuoitav/common/db/couch"
	"github.com/byuoitav/common/log"
	"github.com/byuoitav/common/structs"
	crestrontelnet "github.com/byuoitav/crestron-telnet-microservice/crestron-telnet"
)

var (
	address  = os.Getenv("DB_ADDRESS")
	username = os.Getenv("DB_USERNAME")
	password = os.Getenv("DB_PASSWORD")
)

func init() {
	if len(address) == 0 || len(username) == 0 || len(password) == 0 {
		log.L.Fatalf("One of DB_ADDRESS, DB_USERNAME, DB_PASSWORD is not set. Failing...")
	}
}

func main() {
	router := common.NewRouter()

	port := ":10015"

	server := http.Server{
		Addr:           port,
		MaxHeaderBytes: 1024 * 10,
	}

	log.SetLevel("debug")

	//go launchDMPSMonitors()

	go launchOtherCrestronMonitors()

	err := router.StartServer(&server)
	if err != nil {
		log.L.Fatalf("error running server: %s", err)
	}
}

func launchDMPSMonitors() {

	for {

		db := couch.NewDB(address, username, password)

		dmpsList, err := db.GetDMPSList()

		if err != nil {
			log.L.Fatalf("Error retriving DMPS List %v", err)
		}

		killChannel := make(chan bool, len(dmpsList.List))

		go monitorDMPSList(dmpsList, killChannel)

		var waitG sync.WaitGroup
		waitG.Add(len(dmpsList.List))

		for _, dmps := range dmpsList.List {
			log.L.Debugf("Launching dmps %v", dmps)
			go crestrontelnet.MonitorDMPS(dmps, killChannel, &waitG)
		}

		waitG.Wait()
	}
}

func monitorDMPSList(currentDmpsList structs.DMPSList, killChannel chan bool) {
	for {
		//wait 5 minutes
		log.L.Debugf("Waiting to check for Dmps list changes")
		time.Sleep(5 * time.Minute)

		log.L.Debugf("Checking Dmps List for changes")

		//get list
		db := couch.NewDB(address, username, password)

		dmpsList, err := db.GetDMPSList()

		if err != nil {
			log.L.Warnf("Error retriving DMPS List %v", err)
		}

		needsToRefresh := false

		if len(dmpsList.List) == len(currentDmpsList.List) {
			oldList := make([]structs.DMPS, len(currentDmpsList.List))
			newList := make([]structs.DMPS, len(dmpsList.List))
			copy(oldList, currentDmpsList.List)
			copy(newList, dmpsList.List)
			log.L.Debugf("dmps list compare at start, %v, %v", oldList, newList)

			//compare list
			for i := 0; i < len(oldList); i++ {
				//find this one in the new list
				old := oldList[i]
				match := false

				for j := range newList {
					new := newList[j]

					if old.Hostname == new.Hostname && old.Address == new.Address {
						//match
						match = true
						newList = append(newList[:j], newList[j+1:]...)

						break
					}
				}

				if match {
					oldList = append(oldList[:i], oldList[i+1:]...)
					i--
				}
			}

			log.L.Debugf("dmps list compare at end, %v, %v", oldList, newList)

			if len(oldList) > 0 || len(newList) > 0 {
				log.L.Debugf("dmps list difference, %v, %v", oldList, newList)
				needsToRefresh = true
			}

		} else {
			needsToRefresh = true
			log.L.Debugf("dmps list length difference, %v, %v", len(dmpsList.List), len(currentDmpsList.List))
		}

		if needsToRefresh {
			//send kill signals and return
			for i := 0; i < len(currentDmpsList.List); i++ {
				killChannel <- true
			}

			return
		}
	}
}

func launchOtherCrestronMonitors() {

	for {
		log.L.Debugf("Address %s, User %s", address, username)
		db := couch.NewDB(address, username, password)

		otherCrestronList, err := db.GetOtherCrestronList()

		if err != nil {
			log.L.Fatalf("Error retriving Other Crestron List %v", err)
		}

		killChannel := make(chan bool, len(otherCrestronList.List))

		go monitorOtherCrestronList(otherCrestronList, killChannel)

		var waitG sync.WaitGroup
		waitG.Add(len(otherCrestronList.List))

		for _, dmps := range otherCrestronList.List {
			log.L.Debugf("Launching other crestron %v", dmps)
			go crestrontelnet.MonitorOtherCrestron(dmps, killChannel, &waitG)
		}

		waitG.Wait()
	}
}

func monitorOtherCrestronList(currentList structs.DMPSList, killChannel chan bool) {
	for {
		//wait 5 minutes
		log.L.Debugf("Waiting to check for other crestron list changes")
		time.Sleep(5 * time.Minute)

		log.L.Debugf("Checking other crestron List for changes")

		//get list
		db := couch.NewDB(address, username, password)

		dmpsList, err := db.GetOtherCrestronList()

		if err != nil {
			log.L.Warnf("Error retriving other crestron List %v", err)
		}

		needsToRefresh := false

		if len(dmpsList.List) == len(currentList.List) {
			oldList := make([]structs.DMPS, len(currentList.List))
			newList := make([]structs.DMPS, len(dmpsList.List))
			copy(oldList, currentList.List)
			copy(newList, dmpsList.List)
			log.L.Debugf("dmps list compare at start, %v, %v", oldList, newList)

			//compare list
			for i := 0; i < len(oldList); i++ {
				//find this one in the new list
				old := oldList[i]
				match := false

				for j := range newList {
					new := newList[j]

					if old.Hostname == new.Hostname && old.Address == new.Address {
						//match
						match = true
						newList = append(newList[:j], newList[j+1:]...)

						break
					}
				}

				if match {
					oldList = append(oldList[:i], oldList[i+1:]...)
					i--
				}
			}

			log.L.Debugf("dmps list compare at end, %v, %v", oldList, newList)

			if len(oldList) > 0 || len(newList) > 0 {
				log.L.Debugf("dmps list difference, %v, %v", oldList, newList)
				needsToRefresh = true
			}

		} else {
			needsToRefresh = true
			log.L.Debugf("dmps list length difference, %v, %v", len(dmpsList.List), len(currentList.List))
		}

		if needsToRefresh {
			//send kill signals and return
			for i := 0; i < len(currentList.List); i++ {
				killChannel <- true
			}

			return
		}
	}
}
