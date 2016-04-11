package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/goraft/raft"
	"github.com/gorilla/mux"
)

// The interface of Cluster
type ICluster interface {
	Start(string) error

	StateChangeEventHandler(raft.Event)
	LeaderChangeEventHandler(raft.Event)
	TermChangeEventHandler(raft.Event)

	RegisterService(io.ReadCloser) error
	IsCurrentServiceLeader() bool
	GetLeaderConnectionString() (string, error)

	DescribeState() (string, error)
}

// The definition of Cluster
type Cluster struct {
	serviceID  string          `json:"service_id"`
	host       string          `json:"host"`
	port       int             `json:"port"`
	path       string          `json:"path"`
	router     *mux.Router     `json:"mux_router"`
	raftServer raft.Server     `json:"raft_server"`
	peers      map[string]bool `json:"peers"`
}

// The const used to check state of service
const (
	ClusterStatusCheckInterval = 10 // in seconds
	MaxHeartbeatFailTimes      = 5
)

// The constructor of Cluster
func New(serviceId string, host string, port int, path string, router *mux.Router) *Cluster {
	return &Cluster{
		serviceID: serviceId,
		host:      host,
		port:      port,
		path:      path,
		router:    router,
		peers:     make(map[string]bool),
	}
}

// Start the cluster
func (this *Cluster) Start(leader string) error {
	var err error

	logrus.Print("Initializing Raft Server")

	// Initialize and start Raft server.
	transporter := raft.NewHTTPTransporter("/raft", 200*time.Millisecond)
	this.raftServer, err = raft.NewServer(this.serviceID, this.path, transporter, nil, nil, "")
	if err != nil {
		logrus.Fatal(err)
	}
	transporter.Install(this.raftServer, this)

	this.raftServer.AddEventListener(raft.StateChangeEventType, this.StateChangeEventHandler)
	this.raftServer.AddEventListener(raft.LeaderChangeEventType, this.LeaderChangeEventHandler)
	this.raftServer.AddEventListener(raft.TermChangeEventType, this.TermChangeEventHandler)

	this.raftServer.Start()

	// Join to the cluster
	if leader != "" {
		// Join to leader if specified.
		logrus.Println("Attempting to join leader:", leader)

		if !this.raftServer.IsLogEmpty() {
			logrus.Fatal("Cannot join with an existing log")
		}
		if err := this.join(leader); err != nil {
			logrus.Fatal(err)
		}

	} else if this.raftServer.IsLogEmpty() {
		// Initialize the server by joining itself.
		logrus.Println("Initializing new cluster")

		_, err := this.raftServer.Do(&raft.DefaultJoinCommand{
			Name:             this.raftServer.Name(),
			ConnectionString: this.connectionString(),
		})
		if err != nil {
			logrus.Fatal(err)
		}

	} else {
		logrus.Println("Recovered from log")
	}
	return err
}

// Returns the connection string.
func (this *Cluster) connectionString() string {
	return fmt.Sprintf("http://%s:%d", this.host, this.port)
}

// Joins to the leader of an existing cluster.
func (this *Cluster) join(leader string) error {
	command := &raft.DefaultJoinCommand{
		Name:             this.raftServer.Name(),
		ConnectionString: this.connectionString(),
	}

	var b bytes.Buffer
	json.NewEncoder(&b).Encode(command)
	resp, err := http.Post(fmt.Sprintf("http://%s/cluster/join", leader), "application/json", &b)
	if err != nil {
		logrus.Error(err)
		return err
	}

	if resp.StatusCode == http.StatusTemporaryRedirect {
		logrus.Debugf("Redirect to %s", resp.Header.Get("Location"))
		var body bytes.Buffer
		json.NewEncoder(&body).Encode(command)
		_, err := http.Post(resp.Header.Get("Location"), "application/json", &body)
		if err != nil {
			return err
		}
	}
	return nil
}

// Register Service
func (this *Cluster) RegisterService(body io.ReadCloser) error {
	defer body.Close()

	command := &raft.DefaultJoinCommand{}

	if err := json.NewDecoder(body).Decode(&command); err != nil {
		return err
	}
	if _, err := this.raftServer.Do(command); err != nil {
		return err
	}
	return nil
}

// This is a hack around Gorilla mux not providing the correct net/http HandleFunc() interface.
func (this *Cluster) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	this.router.HandleFunc(pattern, handler)
}

func (this *Cluster) StateChangeEventHandler(e raft.Event) {
	server := e.Source().(raft.Server)
	logrus.Printf("[%s] %s %v -> %v\n", server.Name(), e.Type(), e.PrevValue(), e.Value())
}

func (this *Cluster) LeaderChangeEventHandler(e raft.Event) {
	go this.LeaderChange(e)
}

func (this *Cluster) TermChangeEventHandler(e raft.Event) {
	server := e.Source().(raft.Server)
	logrus.Printf("[%s] %s %v -> %v\n", server.Name(), e.Type(), e.PrevValue(), e.Value())
}

func (this *Cluster) LeaderChange(e raft.Event) {
	server := e.Source().(raft.Server)
	logrus.Printf("[%s] %s %v -> %v", server.Name(), e.Type(), e.PrevValue(), e.Value())

	if this.IsCurrentServiceLeader() {
		logrus.Println("Start to perform leader tasks.")
		// Perform task
		this.checkPeersStatus()
	}
}

// Check the services status, update the state if online, or set offline if failure.
func (this *Cluster) checkPeersStatus() {
	ticker := time.NewTicker(time.Second * ClusterStatusCheckInterval)
	for _ = range ticker.C {
		if !this.IsCurrentServiceLeader() {
			return
		}

		logrus.Debugf("Check Peers Status: MemberCount [%v], Peers Count [%v]",
			this.raftServer.MemberCount(), len(this.raftServer.Peers()))

		for _, peer := range this.raftServer.Peers() {
			if this.isPeerOffline(peer) {
				// Become OFFLINE
				if connected, ok := this.peers[peer.Name]; !ok || connected {
					this.peers[peer.Name] = false
					go this.transferOrders(peer.Name)
				}
			} else {
				this.peers[peer.Name] = true
			}
			logrus.Debugf("[%v]Peer [%v]", this.peers[peer.Name], peer)

			if !this.IsCurrentServiceLeader() {
				return
			}
		}
	}
}

// Check whether peer is offline
func (this *Cluster) isPeerOffline(peer *raft.Peer) bool {
	elapsedTime := time.Now().Sub(peer.LastActivity())
	if elapsedTime > time.Duration(float64(raft.DefaultHeartbeatInterval)*MaxHeartbeatFailTimes) {
		return true
	}
	return false
}

// Check whether current service is leader
func (this *Cluster) IsCurrentServiceLeader() bool {
	return this.raftServer.State() == raft.Leader // this.raftServer.Name() == this.raftServer.Leader()
}

// Transfer the orders of one offline service
func (this *Cluster) transferOrders(serviceId string) {
	if serviceId == this.serviceID {
		logrus.Fatal("Cannot tranfer self orders when alive")
	}

	transfer := func(connectionString string, client *http.Client) bool {
		data := map[string]string{
			"service_id": serviceId,
		}
		jsonData, _ := json.Marshal(data)
		body := strings.NewReader(string(jsonData))
		req, _ := http.NewRequest("POST", connectionString+"/service/transfer", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "user")
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			return true
		}
		return false
	}

	transferred := false
	client := &http.Client{}
	for !transferred && this.IsCurrentServiceLeader() {
		// select one online service and transfer the pending orders
		// TODO Random select
		for _, peer := range this.raftServer.Peers() {
			if !this.isPeerOffline(peer) {
				if peer.Name == serviceId {
					logrus.Debug("Abort tranfer orders when service recovery")
					transferred = true
					break
				}
				transferred = transfer(peer.ConnectionString, client)
				if transferred {
					break
				}
			}
		}

		if !transferred {
			transferred = transfer(this.connectionString(), client)
		}
	}
}

// Return of connection string of raft cluster leader
func (this *Cluster) GetLeaderConnectionString() (string, error) {
	if this.IsCurrentServiceLeader() {
		return this.connectionString(), nil
	}

	if peer, ok := this.raftServer.Peers()[this.raftServer.Leader()]; ok {
		return peer.ConnectionString, nil
	}
	return "", errors.New("Retrieve leader connection string failed")
}

// Describe the cluster state
func (this *Cluster) DescribeState() (string, error) {
	nodesMap := []map[string]interface{}{}

	generatePeerInfo := func(name string, connStr string, last time.Time, connected bool) map[string]interface{} {
		return map[string]interface{}{
			"name":              name,
			"connection_string": connStr,
			"last_activity":     last,
			"connected":         connected,
		}
	}

	for _, peer := range this.raftServer.Peers() {
		nodesMap = append(nodesMap, generatePeerInfo(peer.Name,
			peer.ConnectionString, peer.LastActivity(), !this.isPeerOffline(peer)))
	}

	nodesMap = append(nodesMap, generatePeerInfo(this.raftServer.Name(),
		this.connectionString(), time.Now(), true))

	statusMap := map[string]interface{}{
		"leader_name":  this.raftServer.Leader(),
		"nodes_count":  this.raftServer.MemberCount(),
		"nodes":        nodesMap,
		"generated_at": time.Now().String(),
	}

	str, err := json.Marshal(&statusMap)
	if err != nil {
		return "", err
	}
	return string(str), nil
}
