package rrdtool

import (
	"encoding/base64"
	"errors"
	"io/ioutil"
	"log"
	"net/rpc"
	"net/rpc/jsonrpc"
	"sync/atomic"
	"time"

	"stathat.com/c/consistent"

	cmodel "github.com/open-falcon/common/model"
	"github.com/open-falcon/graph/g"
	"github.com/open-falcon/graph/store"
)

type Task_ch_t struct {
	Method string
	Key    string
	Done   chan error
	Args   interface{}
	Reply  interface{}
}

var (
	Consistent       *consistent.Consistent
	Task_ch          map[string]chan Task_ch_t
	clients          map[string][]*rpc.Client
	flushrrd_timeout int32
)

func init() {
	Consistent = consistent.New()
	Task_ch = make(map[string]chan Task_ch_t)
	clients = make(map[string][]*rpc.Client)
}

func migrate_start(cfg *g.GlobalConfig) {
	var err error
	var i int
	var client *rpc.Client
	if cfg.Migrate.Enabled {
		Consistent.NumberOfReplicas = cfg.Migrate.Replicas

		for node, addr := range cfg.Migrate.Cluster {
			Consistent.Add(node)
			Task_ch[node] = make(chan Task_ch_t, 1)
			clients[node] = make([]*rpc.Client, cfg.Migrate.Concurrency)

			for i = 0; i < cfg.Migrate.Concurrency; i++ {
				if client, err = jsonrpc.Dial("tcp", addr); err != nil {
					log.Fatalf("node:%s addr:%s err:%s\n", node, addr, err)
				}
				clients[node][i] = client
				go task_worker(i, Task_ch[node], client, node, addr)
			}
		}
	}
}

func task_worker(idx int, ch chan Task_ch_t, client *rpc.Client, node, addr string) {
	var err error
	for {
		select {
		case task := <-ch:
			if task.Method == "Graph.Send" {
				err = send_data(client, task.Key, addr)
			} else if task.Method == "Graph.Query" {
				err = query_data(client, addr, task.Args, task.Reply)
			} else {
				if atomic.LoadInt32(&flushrrd_timeout) != 0 {
					// hope this more faster than fetch_rrd
					err = send_data(client, task.Key, addr)
				} else {
					err = fetch_rrd(client, task.Key, addr)
				}
			}
			if task.Done != nil {
				task.Done <- err
			}
		}
	}
}

func reconnection(client *rpc.Client, addr string) {
	var err error
	client.Close()
	client, err = jsonrpc.Dial("tcp", addr)
	for err != nil {
		//danger!! block routine
		time.Sleep(time.Millisecond * 500)
		client, err = jsonrpc.Dial("tcp", addr)
	}
}

func query_data(client *rpc.Client, addr string,
	args interface{}, resp interface{}) error {
	var (
		err error
	)

	err = Jsonrpc_call(client, "Graph.Query", args, resp,
		time.Duration(g.Config().CallTimeout)*time.Millisecond)

	if err != nil {
		reconnection(client, addr)
	}
	return err
}

func send_data(client *rpc.Client, key string, addr string) error {
	var (
		err  error
		flag uint32
		resp *cmodel.SimpleRpcResponse
	)

	//remote
	if flag, err = store.GraphItems.GetFlag(key); err != nil {
		return err
	}
	cfg := g.Config()

	store.GraphItems.SetFlag(key, flag|g.GRAPH_F_SENDING)

	items := store.GraphItems.PopAll(key)
	items_size := len(items)
	if items_size == 0 {
		goto out
	}
	resp = &cmodel.SimpleRpcResponse{}

	err = Jsonrpc_call(client, "Graph.Send", items, resp,
		time.Duration(cfg.CallTimeout)*time.Millisecond)

	if err != nil {
		store.GraphItems.PushAll(key, items)
		reconnection(client, addr)
		goto err_out
	}
	goto out

err_out:
	flag |= g.GRAPH_F_ERR
out:
	flag &= ^g.GRAPH_F_SENDING
	store.GraphItems.SetFlag(key, flag)
	return err

}

func fetch_rrd(client *rpc.Client, key string, addr string) error {
	var (
		err      error
		flag     uint32
		md5      string
		dsType   string
		filename string
		step     int
		rrdfile  g.File64
		ctx      []byte
	)

	cfg := g.Config()

	if flag, err = store.GraphItems.GetFlag(key); err != nil {
		return err
	}

	store.GraphItems.SetFlag(key, flag|g.GRAPH_F_FETCHING)

	md5, dsType, step, _ = g.SplitRrdCacheKey(key)
	filename = g.RrdFileName(cfg.RRD.Storage, md5, dsType, step)

	items := store.GraphItems.PopAll(key)
	items_size := len(items)
	if items_size == 0 {
		// impossible
		goto out
	}

	err = Jsonrpc_call(client, "Graph.GetRrd", key, &rrdfile,
		time.Duration(cfg.CallTimeout)*time.Millisecond)

	if err != nil {
		store.GraphItems.PushAll(key, items)
		reconnection(client, addr)
		goto err_out
	}

	if ctx, err = base64.StdEncoding.DecodeString(rrdfile.Body64); err != nil {
		store.GraphItems.PushAll(key, items)
		goto err_out
	} else {
		if err = ioutil.WriteFile(filename, ctx, 0644); err != nil {
			store.GraphItems.PushAll(key, items)
			goto err_out
		} else {
			flag &= ^g.GRAPH_F_MISS
			Flush(filename, items)
			goto out
		}
	}
	//noneed
	goto out

err_out:
	flag |= g.GRAPH_F_ERR
out:
	flag &= ^g.GRAPH_F_FETCHING
	store.GraphItems.SetFlag(key, flag)
	return err
}

func Jsonrpc_call(client *rpc.Client, method string, args interface{},
	reply interface{}, timeout time.Duration) error {
	done := make(chan *rpc.Call, 1)
	client.Go(method, args, reply, done)
	select {
	case <-time.After(timeout):
		return errors.New("timeout")
	case call := <-done:
		if call.Error == nil {
			return nil
		} else {
			return call.Error
		}
	}
}
