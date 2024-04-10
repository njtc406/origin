package cluster

import (
	"errors"
	"github.com/duanhf2012/origin/v2/log"
	"github.com/duanhf2012/origin/v2/rpc"
	"github.com/duanhf2012/origin/v2/service"
	"time"
	"github.com/duanhf2012/origin/v2/util/timer"
	"google.golang.org/protobuf/proto"
)

const DynamicDiscoveryMasterName = "DiscoveryMaster"
const DynamicDiscoveryClientName = "DiscoveryClient"
const RegServiceDiscover = DynamicDiscoveryMasterName + ".RPC_RegServiceDiscover"
const SubServiceDiscover = DynamicDiscoveryClientName + ".RPC_SubServiceDiscover"
const AddSubServiceDiscover = DynamicDiscoveryMasterName + ".RPC_AddSubServiceDiscover"
const NodeRetireRpcMethod = DynamicDiscoveryMasterName+".RPC_NodeRetire"

type DynamicDiscoveryMaster struct {
	service.Service

	mapNodeInfo map[string]struct{}
	nodeInfo    []*rpc.NodeInfo
}

type DynamicDiscoveryClient struct {
	service.Service

	funDelService FunDelNode
	funSetService FunSetNodeInfo
	localNodeId   string

	mapDiscovery map[string]map[string]struct{} //map[masterNodeId]map[nodeId]struct{}
	bRetire bool
}

var masterService DynamicDiscoveryMaster
var clientService DynamicDiscoveryClient

func getDynamicDiscovery() IServiceDiscovery {
	return &clientService
}

func init() {
	masterService.SetName(DynamicDiscoveryMasterName)
	clientService.SetName(DynamicDiscoveryClientName)
}

func (ds *DynamicDiscoveryMaster) isRegNode(nodeId string) bool {
	_, ok := ds.mapNodeInfo[nodeId]
	return ok
}

func (ds *DynamicDiscoveryMaster) updateNodeInfo(nInfo *rpc.NodeInfo) {
	if _,ok:= ds.mapNodeInfo[nInfo.NodeId];ok == false {
		return
	}

	nodeInfo := proto.Clone(nInfo).(*rpc.NodeInfo)
	for i:=0;i<len(ds.nodeInfo);i++ {
		if ds.nodeInfo[i].NodeId == nodeInfo.NodeId {
			ds.nodeInfo[i]  = nodeInfo
			break
		}
	}
}

func (ds *DynamicDiscoveryMaster) addNodeInfo(nInfo *rpc.NodeInfo) {
	if len(nInfo.PublicServiceList) == 0 {
		return
	}

	_, ok := ds.mapNodeInfo[nInfo.NodeId]
	if ok == true {
		return
	}
	ds.mapNodeInfo[nInfo.NodeId] = struct{}{}

	nodeInfo := proto.Clone(nInfo).(*rpc.NodeInfo)
	ds.nodeInfo = append(ds.nodeInfo, nodeInfo)
}

func (ds *DynamicDiscoveryMaster) removeNodeInfo(nodeId string) {
	if _,ok:= ds.mapNodeInfo[nodeId];ok == false {
		return
	}

	for i:=0;i<len(ds.nodeInfo);i++ {
		if ds.nodeInfo[i].NodeId == nodeId {
			ds.nodeInfo = append(ds.nodeInfo[:i],ds.nodeInfo[i+1:]...)
			break
		}
	}

	delete(ds.mapNodeInfo,nodeId)
}

func (ds *DynamicDiscoveryMaster) OnInit() error {
	ds.mapNodeInfo = make(map[string]struct{}, 20)
	ds.RegRpcListener(ds)

	return nil
}

func (ds *DynamicDiscoveryMaster) OnStart() {
	var nodeInfo rpc.NodeInfo
	localNodeInfo := cluster.GetLocalNodeInfo()
	nodeInfo.NodeId = localNodeInfo.NodeId
	nodeInfo.ListenAddr = localNodeInfo.ListenAddr
	nodeInfo.PublicServiceList = localNodeInfo.PublicServiceList
	nodeInfo.MaxRpcParamLen = localNodeInfo.MaxRpcParamLen
	nodeInfo.Private = localNodeInfo.Private
	nodeInfo.Retire = localNodeInfo.Retire

	ds.addNodeInfo(&nodeInfo)
}

func (ds *DynamicDiscoveryMaster) OnNodeConnected(nodeId string) {
	//没注册过结点不通知
	if ds.isRegNode(nodeId) == false {
		return
	}

	//向它发布所有服务列表信息
	var notifyDiscover rpc.SubscribeDiscoverNotify
	notifyDiscover.IsFull = true
	notifyDiscover.NodeInfo = ds.nodeInfo
	notifyDiscover.MasterNodeId = cluster.GetLocalNodeInfo().NodeId

	ds.GoNode(nodeId, SubServiceDiscover, &notifyDiscover)
}

func (ds *DynamicDiscoveryMaster) OnNodeDisconnect(nodeId string) {
	if ds.isRegNode(nodeId) == false {
		return
	}

	ds.removeNodeInfo(nodeId)

	var notifyDiscover rpc.SubscribeDiscoverNotify
	notifyDiscover.MasterNodeId = cluster.GetLocalNodeInfo().NodeId
	notifyDiscover.DelNodeId = nodeId
	//删除结点
	cluster.DelNode(nodeId, true)

	//无注册过的结点不广播，避免非当前Master网络中的连接断开时通知到本网络
	ds.CastGo(SubServiceDiscover, &notifyDiscover)
}

func (ds *DynamicDiscoveryMaster) RpcCastGo(serviceMethod string, args interface{}) {
	for nodeId, _ := range ds.mapNodeInfo {
		ds.GoNode(nodeId, serviceMethod, args)
	}
}

func (ds *DynamicDiscoveryMaster) RPC_NodeRetire(req *rpc.NodeRetireReq, res *rpc.Empty) error {
	log.Info("node is retire",log.String("nodeId",req.NodeInfo.NodeId),log.Bool("retire",req.NodeInfo.Retire))

	ds.updateNodeInfo(req.NodeInfo)

	var notifyDiscover rpc.SubscribeDiscoverNotify
	notifyDiscover.MasterNodeId = cluster.GetLocalNodeInfo().NodeId
	notifyDiscover.NodeInfo = append(notifyDiscover.NodeInfo, req.NodeInfo)
	ds.RpcCastGo(SubServiceDiscover, &notifyDiscover)

	return nil
}

// 收到注册过来的结点
func (ds *DynamicDiscoveryMaster) RPC_RegServiceDiscover(req *rpc.ServiceDiscoverReq, res *rpc.Empty) error {
	if req.NodeInfo == nil {
		err := errors.New("RPC_RegServiceDiscover req is error.")
		log.Error(err.Error())

		return err
	}

	//广播给其他所有结点
	var notifyDiscover rpc.SubscribeDiscoverNotify
	notifyDiscover.MasterNodeId = cluster.GetLocalNodeInfo().NodeId
	notifyDiscover.NodeInfo = append(notifyDiscover.NodeInfo, req.NodeInfo)
	ds.RpcCastGo(SubServiceDiscover, &notifyDiscover)

	//存入本地
	ds.addNodeInfo(req.NodeInfo)

	//初始化结点信息
	var nodeInfo NodeInfo
	nodeInfo.NodeId = req.NodeInfo.NodeId
	nodeInfo.Private = req.NodeInfo.Private
	nodeInfo.ServiceList = req.NodeInfo.PublicServiceList
	nodeInfo.PublicServiceList = req.NodeInfo.PublicServiceList
	nodeInfo.ListenAddr = req.NodeInfo.ListenAddr
	nodeInfo.MaxRpcParamLen = req.NodeInfo.MaxRpcParamLen
	nodeInfo.Retire = req.NodeInfo.Retire

	//主动删除已经存在的结点,确保先断开，再连接
	cluster.serviceDiscoveryDelNode(nodeInfo.NodeId, true)

	//加入到本地Cluster模块中，将连接该结点
	cluster.serviceDiscoverySetNodeInfo(&nodeInfo)

	return nil
}

func (dc *DynamicDiscoveryClient) OnInit() error {
	dc.RegRpcListener(dc)
	dc.mapDiscovery = map[string]map[string]struct{}{}
	return nil
}

func (dc *DynamicDiscoveryClient) addMasterNode(masterNodeId string, nodeId string) {
	_, ok := dc.mapDiscovery[masterNodeId]
	if ok == false {
		dc.mapDiscovery[masterNodeId] = map[string]struct{}{}
	}
	dc.mapDiscovery[masterNodeId][nodeId] = struct{}{}
}

func (dc *DynamicDiscoveryClient) removeMasterNode(masterNodeId string, nodeId string) {
	mapNodeId, ok := dc.mapDiscovery[masterNodeId]
	if ok == false {
		return
	}

	delete(mapNodeId, nodeId)
}

func (dc *DynamicDiscoveryClient) findNodeId(nodeId string) bool {
	for _, mapNodeId := range dc.mapDiscovery {
		_, ok := mapNodeId[nodeId]
		if ok == true {
			return true
		}
	}

	return false
}

func (dc *DynamicDiscoveryClient) OnStart() {
	//2.添加并连接发现主结点
	dc.addDiscoveryMaster()
}

func (dc *DynamicDiscoveryClient) addDiscoveryMaster() {
	discoveryNodeList := cluster.GetDiscoveryNodeList()
	for i := 0; i < len(discoveryNodeList); i++ {
		if discoveryNodeList[i].NodeId == cluster.GetLocalNodeInfo().NodeId {
			continue
		}
		dc.funSetService(&discoveryNodeList[i])
	}
}

func (dc *DynamicDiscoveryClient) fullCompareDiffNode(masterNodeId string, mapNodeInfo map[string]*rpc.NodeInfo) []string {
	if mapNodeInfo == nil {
		return nil
	}

	diffNodeIdSlice := make([]string, 0, len(mapNodeInfo))
	mapNodeId := map[string]struct{}{}
	mapNodeId, ok := dc.mapDiscovery[masterNodeId]
	if ok == false {
		return nil
	}

	//本地任何Master都不存在的，放到diffNodeIdSlice
	for nodeId, _ := range mapNodeId {
		_, ok := mapNodeInfo[nodeId]
		if ok == false {
			diffNodeIdSlice = append(diffNodeIdSlice, nodeId)
		}
	}

	return diffNodeIdSlice
}

//订阅发现的服务通知
func (dc *DynamicDiscoveryClient) RPC_SubServiceDiscover(req *rpc.SubscribeDiscoverNotify) error {
	mapNodeInfo := map[string]*rpc.NodeInfo{}
	for _, nodeInfo := range req.NodeInfo {
		//不对本地结点或者不存在任何公开服务的结点
		if nodeInfo.NodeId == dc.localNodeId {
			continue
		}

		if cluster.IsMasterDiscoveryNode() == false && len(nodeInfo.PublicServiceList) == 1 &&
			nodeInfo.PublicServiceList[0] == DynamicDiscoveryClientName {
			continue
		}

		//遍历所有的公开服务，并筛选之
		for _, serviceName := range nodeInfo.PublicServiceList {
			nInfo := mapNodeInfo[nodeInfo.NodeId]
			if nInfo == nil {
				nInfo = &rpc.NodeInfo{}
				nInfo.NodeId = nodeInfo.NodeId
				nInfo.ListenAddr = nodeInfo.ListenAddr
				nInfo.MaxRpcParamLen = nodeInfo.MaxRpcParamLen
				nInfo.Retire = nodeInfo.Retire
				nInfo.Private = nodeInfo.Private

				mapNodeInfo[nodeInfo.NodeId] = nInfo
			}

			nInfo.PublicServiceList = append(nInfo.PublicServiceList, serviceName)
		}
	}

	//如果为完整同步，则找出差异的结点
	var willDelNodeId []string
	if req.IsFull == true {
		diffNode := dc.fullCompareDiffNode(req.MasterNodeId, mapNodeInfo)
		if len(diffNode) > 0 {
			willDelNodeId = append(willDelNodeId, diffNode...)
		}
	}

	//指定删除结点
	if req.DelNodeId != rpc.NodeIdNull && req.DelNodeId != dc.localNodeId {
		willDelNodeId = append(willDelNodeId, req.DelNodeId)
	}

	//删除不必要的结点
	for _, nodeId := range willDelNodeId {
		cluster.TriggerDiscoveryEvent(false,nodeId,nil)
		dc.removeMasterNode(req.MasterNodeId, nodeId)
		if dc.findNodeId(nodeId) == false {
			dc.funDelService(nodeId, false)
		}
	}

	//设置新结点
	for _, nodeInfo := range mapNodeInfo {
		bSet := dc.setNodeInfo(req.MasterNodeId,nodeInfo)
		if bSet == false {
			continue
		}

		cluster.TriggerDiscoveryEvent(true,nodeInfo.NodeId,nodeInfo.PublicServiceList)
	}

	return nil
}

func (dc *DynamicDiscoveryClient) isDiscoverNode(nodeId string) bool {
	for i := 0; i < len(cluster.masterDiscoveryNodeList); i++ {
		if cluster.masterDiscoveryNodeList[i].NodeId == nodeId {
			return true
		}
	}

	return false
}

func (dc *DynamicDiscoveryClient) OnNodeConnected(nodeId string) {
	dc.regServiceDiscover(nodeId)
}

func (dc *DynamicDiscoveryClient) OnRetire(){
	dc.bRetire = true

	masterNodeList := cluster.GetDiscoveryNodeList()
	for i:=0;i<len(masterNodeList);i++{
		var nodeRetireReq rpc.NodeRetireReq

		nodeRetireReq.NodeInfo = &rpc.NodeInfo{}
		nodeRetireReq.NodeInfo.NodeId = cluster.localNodeInfo.NodeId
		nodeRetireReq.NodeInfo.ListenAddr = cluster.localNodeInfo.ListenAddr
		nodeRetireReq.NodeInfo.MaxRpcParamLen = cluster.localNodeInfo.MaxRpcParamLen
		nodeRetireReq.NodeInfo.PublicServiceList =  cluster.localNodeInfo.PublicServiceList
		nodeRetireReq.NodeInfo.Retire = dc.bRetire
		nodeRetireReq.NodeInfo.Private = cluster.localNodeInfo.Private

		err := dc.GoNode(masterNodeList[i].NodeId,NodeRetireRpcMethod,&nodeRetireReq)
		if err!= nil {
			log.Error("call "+NodeRetireRpcMethod+" is fail",log.ErrorAttr("err",err))
		}
	}
}

func (dc *DynamicDiscoveryClient) regServiceDiscover(nodeId string){
	nodeInfo := cluster.GetMasterDiscoveryNodeInfo(nodeId)
	if nodeInfo == nil {
		return
	}

	var req rpc.ServiceDiscoverReq
	req.NodeInfo = &rpc.NodeInfo{}
	req.NodeInfo.NodeId = cluster.localNodeInfo.NodeId
	req.NodeInfo.ListenAddr = cluster.localNodeInfo.ListenAddr
	req.NodeInfo.MaxRpcParamLen = cluster.localNodeInfo.MaxRpcParamLen
	req.NodeInfo.PublicServiceList =  cluster.localNodeInfo.PublicServiceList
	req.NodeInfo.Retire = dc.bRetire
	req.NodeInfo.Private = cluster.localNodeInfo.Private

	//向Master服务同步本Node服务信息
	err := dc.AsyncCallNode(nodeId, RegServiceDiscover, &req, func(res *rpc.Empty, err error) {
		if err != nil {
			log.Error("call "+RegServiceDiscover+" is fail :"+ err.Error())
			dc.AfterFunc(time.Second*3, func(timer *timer.Timer) {
				dc.regServiceDiscover(nodeId)
			})

			return
		}
	})
	if err != nil {
		log.Error("call "+ RegServiceDiscover+" is fail :"+ err.Error())
	}
}

func (dc *DynamicDiscoveryClient) canDiscoveryService(fromMasterNodeId string,serviceName string) bool{
	canDiscovery := true

	for i:=0;i<len(cluster.GetLocalNodeInfo().MasterDiscoveryService);i++{
		masterNodeId := cluster.GetLocalNodeInfo().MasterDiscoveryService[i].MasterNodeId

		if masterNodeId == fromMasterNodeId || masterNodeId == rpc.NodeIdNull {
			canDiscovery = false

			for _,discoveryService := range cluster.GetLocalNodeInfo().MasterDiscoveryService[i].DiscoveryService {
				if discoveryService == serviceName {
					return true
				}
			}
		}
	}

	return canDiscovery
}

func (dc *DynamicDiscoveryClient) setNodeInfo(masterNodeId string,nodeInfo *rpc.NodeInfo) bool{
	if nodeInfo == nil || nodeInfo.Private == true || nodeInfo.NodeId == dc.localNodeId {
		return false
	}

	//筛选关注的服务
	var discoverServiceSlice = make([]string, 0, 24)
	for _, pubService := range nodeInfo.PublicServiceList {
		if dc.canDiscoveryService(masterNodeId,pubService) == true {
			discoverServiceSlice = append(discoverServiceSlice,pubService)
		}
	}

	if len(discoverServiceSlice) == 0 {
		return false
	}

	var nInfo NodeInfo
	nInfo.ServiceList = discoverServiceSlice
	nInfo.PublicServiceList = discoverServiceSlice
	nInfo.NodeId = nodeInfo.NodeId
	nInfo.ListenAddr = nodeInfo.ListenAddr
	nInfo.MaxRpcParamLen = nodeInfo.MaxRpcParamLen
	nInfo.Retire = nodeInfo.Retire
	nInfo.Private = nodeInfo.Private

	dc.funSetService(&nInfo)

	return true
}

func (dc *DynamicDiscoveryClient) OnNodeDisconnect(nodeId string) {
	//将Discard结点清理
	cluster.DiscardNode(nodeId)
}

func (dc *DynamicDiscoveryClient) InitDiscovery(localNodeId string, funDelNode FunDelNode, funSetNodeInfo FunSetNodeInfo) error {
	dc.localNodeId = localNodeId
	dc.funDelService = funDelNode
	dc.funSetService = funSetNodeInfo

	return nil
}

func (dc *DynamicDiscoveryClient) OnNodeStop() {
}
