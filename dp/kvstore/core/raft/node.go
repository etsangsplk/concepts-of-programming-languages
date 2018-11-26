package raft

import (
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"
)

// Node is a node in a Raft consensus cluster. It is called "server" in the original Raft paper.
// Node seems to be more accurate because we can run multiple nodes in a single server process.
type Node struct {
	id             int
	statemachine   *Statemachine
	replicatedLog  *ReplicatedLog
	electionTimer  *time.Timer // runs only if the node is FOLLOWER or CANDIDATE
	heartbeatTimer *time.Timer // runs only if the node is in LEADER state
	currentTerm    int
	votedFor       *int
	cluster        *Cluster
}

// NewNode constructor. Id starts with 0 for the first node and should be +1 for the next node.
func NewNode(id int) *Node {
	node := new(Node)
	node.id = id
	node.currentTerm = 0
	node.votedFor = nil
	node.statemachine = NewStatemachine()
	node.replicatedLog = NewReplicatedLog()
	return node
}

// Start initializes the election timer.
func (n *Node) Start(cluster *Cluster) {
	n.cluster = cluster
	n.resetElectionTimer()
}

// Stop stops all running timers.
func (n *Node) Stop() {
	if n.statemachine.Current() == LEADER {
		if n.heartbeatTimer != nil {
			n.heartbeatTimer.Stop()
		}
	} else {
		if n.electionTimer != nil {
			n.electionTimer.Stop()
		}
	}
}

// resetElectionTimer initializes or restarts a random timer.
func (n *Node) resetElectionTimer() {
	if n.electionTimer != nil {
		n.electionTimer.Stop()
	}
	n.electionTimer = time.NewTimer(time.Duration(1000+rand.Intn(2000)) * time.Millisecond)
	go func() {
		<-n.electionTimer.C
		n.electionTimeout()
	}()
}

// startHeartbeat starts an heartbeat and runs forever until the timer ist stopped.
func (n *Node) startHeartbeat() {
	if n.heartbeatTimer == nil {
		n.heartbeatTimer = time.NewTimer(time.Duration(500) * time.Millisecond)
	} else {
		n.heartbeatTimer.Reset(time.Duration(500) * time.Millisecond)
	}
	go func() {
		<-n.heartbeatTimer.C
		n.sendHeartbeat()
		n.startHeartbeat()
	}()
}

// sendHeartbeat
func (n *Node) sendHeartbeat() {
	if n.statemachine.current != LEADER {
		panic("setHeatbeat should only be called on a LEADER")
	}
	n.log("-> Heartbeat")

	rpcIfs := n.cluster.GetFollowers(n.id)
	var wg sync.WaitGroup
	result := make([]bool, len(rpcIfs))
	wg.Add(len(rpcIfs))
	for i, rpcIf := range rpcIfs {
		go func(w *sync.WaitGroup, i int, nodeRPC NodeRPC) {
			term, ok := nodeRPC.AppendEntries(n.currentTerm, n.id, 0, 0, nil, 0)
			if term > n.currentTerm {
				// todo
			}
			result[i] = ok
			w.Done()
		}(&wg, i, rpcIf)
	}
	wg.Wait() // wait until all nodes have voted

	n.log("<- Heartbeat")
}

// electionTimeout happens when a node receives no heartbeat in a given time period.
func (n *Node) electionTimeout() {
	n.log(fmt.Sprintf("Election timout."))
	if n.statemachine.current == LEADER {
		panic("The election timeout should not happen, when a node is LEADER.")
	}
	n.startElectionProcess()
}

// startElectionProcess sends a RequestVote request to other members in the cluster.
// if successful - we get are the new leader in a new term.
func (n *Node) startElectionProcess() {
	n.statemachine.Next(CANDIDATE)
	n.currentTerm++ // new term starts now
	n.votedFor = nil
	electionWon := n.executeElection()
	if electionWon {
		n.log(fmt.Sprintf("Election won. Now acting as leader."))
		n.switchToLeader()
	} else {
		n.log(fmt.Sprintf("Election was not won. Stopping election timer"))
		n.statemachine.Next(FOLLOWER)
		n.resetElectionTimer() // try again, split vote or cluster down
	}
}

// executeElection executes a leader election by sending RequestVote to other nodes.
// for all other nodes in the cluster RequestVote is sent
func (n *Node) executeElection() bool {
	n.log("-> Election")
	rpcIfs := n.cluster.GetFollowers(n.id)
	var wg sync.WaitGroup
	votes := make([]bool, len(rpcIfs))
	wg.Add(len(rpcIfs))
	for i, rpcIf := range rpcIfs {
		go func(w *sync.WaitGroup, i int, rpcIf NodeRPC) {
			term, ok := rpcIf.RequestVote(n.currentTerm, n.id, 0, 0)
			if term > n.currentTerm {
				// todo
			}
			votes[i] = ok
			w.Done()
		}(&wg, i, rpcIf)
	}
	wg.Wait() // wait until all nodes have voted

	// Count votes
	nbrOfVotes := 0
	for _, vote := range votes {
		if vote {
			nbrOfVotes++
		}
	}
	// If more than 50% respond with true - The election was won!
	electionWon := nbrOfVotes > len(rpcIfs)/2
	n.log(fmt.Sprintf("<- Election: %v", electionWon))
	return electionWon
}

func (n *Node) switchToLeader() {
	n.statemachine.Next(LEADER)
	n.electionTimer.Stop()
	n.electionTimer = nil
	n.startHeartbeat()
}

// NodeRPC server implementation

// AppendEntries implementation is used as heardbeat and log replication.
func (n *Node) AppendEntries(term, leaderID, prevLogIndex, prevLogTermin int, entries []string, leaderCommit int) (currentTerm int, success bool) {
	if term < n.currentTerm {
		return n.currentTerm, false // §5.1
	}

	// heartbeat received in FOLLOWER -> reset election timer!
	if entries == nil || len(entries) == 0 {
		n.log("Heartbeat received. Reset election timer.")
		n.resetElectionTimer()
	} else {
		// todo: replicate logs
		log.Printf("[%v] AppendEntries replicate logs on Node: %v", n.statemachine.Current(), n.id)

	}

	return n.currentTerm, true
}

// RequestVote is called by candidates to gather votes.
// It returns the current term to update the candidate
// It returns true when the candidate received vote.
func (n *Node) RequestVote(term, candidateID, lastLogIndex, lastLogTerm int) (int, bool) {
	// see RequestVoteRPC receiver implementation 1
	if term < n.currentTerm {
		return n.currentTerm, false
	}
	// see RequestVoteRPC receiver implementation 2
	if n.votedFor != nil {
		return n.currentTerm, false
	}

	n.currentTerm = term // ok: we join the master term
	n.votedFor = &candidateID

	n.log(fmt.Sprintf("RequestVote received from Candidate %v. Vote OK.", candidateID))

	return n.currentTerm, true
}

func (n *Node) log(msg string) {
	log.Printf("[%v] [%v] [%v] : %v", n.id, n.statemachine.Current(), n.currentTerm, msg)
}
