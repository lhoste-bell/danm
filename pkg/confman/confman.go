package confman

import (
  "errors"
  "log"
  "strings"
  danmtypes "github.com/nokia/danm/crd/apis/danm/v1"
  "github.com/nokia/danm/pkg/bitarray"
  "github.com/nokia/danm/pkg/metacni"
  metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  "k8s.io/kubernetes/pkg/kubelet/cm/cpuset"
)

func GetTenantConfig() (*danmtypes.TenantConfig, error) {
  danmClient, err := metacni.CreateDanmClient()
  if err != nil {
    return nil, err
  }
  reply, err := danmClient.DanmV1().TenantConfigs().List(metav1.ListOptions{})
  if err != nil {
    return nil, err
  }
  configs := reply.Items
  if len(configs) == 0 {
    return nil, errors.New("TenantNetworks cannot be created without provisioning a TenantConfig first!")
  }
  //TODO: do a namespace based selection later if one generic config does not suffice
  return &configs[0], nil
}

func Reserve(tconf *danmtypes.TenantConfig, iface danmtypes.IfaceProfile) (int,error) {
  allocs := bitarray.NewBitArrayFromBase64(iface.Alloc)
  vnis, err := cpuset.Parse(iface.VniRange)
  if err != nil {
    return 0, errors.New("vniRange for interface:" + iface.Name + " cannot be parsed because:" + err.Error())
  }
  chosenVni := -1
  vniSet := vnis.ToSlice()
  for _, vni := range vniSet {
    if allocs.Get(uint32(vni)) {
      continue
    }
    allocs.Set(uint32(vni))
    iface.Alloc = allocs.Encode()
    chosenVni = vni
    break
  }
  if chosenVni == -1 {
    return 0, errors.New("VNI cannot be allocated from interface profile:" + iface.Name + " because the whole range is already reserved")
  }
  index := getIfaceIndex(tconf, iface.Name, iface.VniType)
  tconf.HostDevices[index] = iface
  err = updateConfigInApi(tconf)
  if err != nil {
    return 0, errors.New("VNI allocation of TenantConfig cannot be updated in the Kubernetes API because:" + err.Error())
  }
  return chosenVni, nil
}

func getIfaceIndex(tconf *danmtypes.TenantConfig, name, vniType string) int {
  for index, iface := range tconf.HostDevices {
    //As HostDevices is a list, the same interface might be added multiple types but with different VNI types
    //We don't want to accidentally overwrite the wrong profile
    if strings.Contains(iface.Name, name) && iface.VniType == vniType {
      return index
    }
  }
  return -1
}

func updateConfigInApi(tconf *danmtypes.TenantConfig) error {
  danmClient, err := metacni.CreateDanmClient()
  if err != nil {
    return err
  }
  confClient := danmClient.DanmV1().TenantConfigs()
  //TODO: now, do we actually need to manually check for the OptimisticLockErrorMessage when we use a generated client,
  //or that is actually dead code in ipam as well?
  //Let's find out!
  _, err = confClient.Update(tconf)
  return err
}

func Free(tconf *danmtypes.TenantConfig, dnet *danmtypes.DanmNet) error {
  if dnet.Spec.Options.Vlan == 0 && dnet.Spec.Options.Vxlan == 0 {
    return nil
  }
  vniType := "vlan"
  if dnet.Spec.Options.Vxlan != 0 {
    vniType = "vxlan"
  }
  ifaceName := dnet.Spec.Options.Device
  if dnet.Spec.Options.DevicePool != "" {
    ifaceName = dnet.Spec.Options.DevicePool
  }
  index := getIfaceIndex(tconf,ifaceName,vniType)
  if index < 0 {
    log.Println("WARNING: There is a data incosistency between TenantNetwork:" + dnet.ObjectMeta.Name + " in namespace:" +
    dnet.ObjectMeta.Namespace + " , and TenantConfig:" + tconf.ObjectMeta.Name +
    " as the used network details are actually not present in TenantConfig. This means your APIs were possibly tampered with!")
    return nil
  }
  allocs := bitarray.NewBitArrayFromBase64(tconf.HostDevices[index].Alloc)
  vni := dnet.Spec.Options.Vlan
  if dnet.Spec.Options.Vxlan != 0 {
    vni = dnet.Spec.Options.Vxlan
  }
  allocs.Reset(uint32(vni))
  tconf.HostDevices[index].Alloc = allocs.Encode()
  return updateConfigInApi(tconf)
}