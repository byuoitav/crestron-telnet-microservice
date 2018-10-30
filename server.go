package main

import (
	"net/http"
	"os"

	"github.com/byuoitav/common"
	"github.com/byuoitav/common/db/couch"
	"github.com/byuoitav/common/log"
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

	//log.SetLevel("debug")

	launchDMPSMonitors()

	err := router.StartServer(&server)
	if err != nil {
		log.L.Fatalf("error running server: %s", err)
	}
}

func launchDMPSMonitors() {
	db := couch.NewDB(address, username, password)

	dmpsList, err := db.GetDMPSList()

	if err != nil {
		log.L.Fatalf("Error retriving DMPS List %v", err)
	}

	for _, dmps := range dmpsList.List {
		log.L.Debugf("Launching dmps %v", dmps)
		go crestrontelnet.MonitorDMPS(dmps)
	}
}
