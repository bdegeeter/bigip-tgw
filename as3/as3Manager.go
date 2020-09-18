package as3

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	slog "github.com/go-eden/slf4go"
	"github.com/xeipuuv/gojsonschema"
)

const as3SupportedVersion = 3.20

/*
var baseAS3Config = `{
	"$schema": "https://raw.githubusercontent.com/F5Networks/f5-appsvcs-extension/master/schema/%s/as3-schema-%s.json",
	"class": "AS3",
	"declaration": {
	  "class": "ADC",
	  "schemaVersion": "%s",
	  "id": "urn:uuid:85626792-9ee7-46bb-8fc8-4ba708cfdc1d",
	  "label": "TGW Declaration",
	  "remark": "Auto-generated by TGW",
	  "controls": {
		 "class": "Controls",
		 "userAgent": "Consul TGW Configured AS3"
	  }
	}
  }
  ` */
var log = slog.NewLogger("as3")

// AS3Manager holds all the AS3 orchestration specific config
type AS3Manager struct {
	as3Validation             bool
	sslInsecure               bool
	enableTLS                 string
	tls13CipherGroupReference string
	ciphers                   string
	// Active User Defined ConfigMap details
	as3ActiveConfig AS3Config
	As3SchemaLatest string
	Schema          string
	// Override existing as3 declaration with this configmap
	//OverriderCfgMapName string
	// Path of schemas reside locally
	//SchemaLocalPath string
	// POSTs configuration to BIG-IP using AS3
	PostManager *PostManager
	// To put list of tenants in BIG-IP REST call URL that are in AS3 declaration
	//FilterTenants    bool
	DefaultPartition string
	ReqChan          chan AS3Config
	RspChan          chan interface{}
	userAgent        string
	//l2l3Agent        L2L3Agent
	//ResourceRequest
	//ResourceResponse
	as3Version                string
	as3Release                string
	unprocessableEntityStatus bool
}

// Struct to allow NewManager to receive all or only specific parameters.
type Params struct {
	// Package local for unit testing only
	Schema                    string
	SchemaVersion             string
	AS3Validation             bool
	SSLInsecure               bool
	EnableTLS                 string
	TLS13CipherGroupReference string
	Ciphers                   string
	//Agent                     string
	OverriderCfgMapName string
	SchemaLocalPath     string
	FilterTenants       bool
	BIGIPUsername       string
	BIGIPPassword       string
	BIGIPURL            string
	TrustedCerts        string
	AS3PostDelay        int
	//ConfigWriter        writer.Writer
	EventChan chan interface{}
	//Log the AS3 response body in Controller logs
	LogResponse               bool
	RspChan                   chan interface{}
	UserAgent                 string
	As3Version                string
	As3Release                string
	unprocessableEntityStatus bool
}

type agentAS3 struct {
	*AS3Manager
}

func CreateAgent() agentAS3 {
	return agentAS3{}
}

func (ag *agentAS3) Init(params Params) error {
	log.Info("[AS3] Initializing AS3 Agent")
	as3Params := params
	ag.AS3Manager = NewAS3Manager(&as3Params)

	ag.ReqChan = make(chan AS3Config, 1)
	if ag.ReqChan != nil {
		go ag.ConfigDeployer()
	}

	err := ag.IsBigIPAppServicesAvailable()
	if err != nil {
		return err
	}
	return nil
}

func (ag *agentAS3) Deploy(req interface{}) error {
	msgReq := req.(AS3Config)
	select {
	case ag.ReqChan <- msgReq:
	case <-ag.ReqChan:
		ag.ReqChan <- msgReq
	}
	return nil
}

func (ag *agentAS3) DeInit() error {
	close(ag.RspChan)
	close(ag.ReqChan)
	return nil
}

// Create and return a new app manager that meets the Manager interface
func NewAS3Manager(params *Params) *AS3Manager {
	as3Manager := AS3Manager{
		as3Validation:             params.AS3Validation,
		sslInsecure:               params.SSLInsecure,
		enableTLS:                 params.EnableTLS,
		tls13CipherGroupReference: params.TLS13CipherGroupReference,
		ciphers:                   params.Ciphers,
		Schema:                    params.Schema,
		//FilterTenants:             params.FilterTenants,
		RspChan:    params.RspChan,
		userAgent:  params.UserAgent,
		as3Version: params.As3Version,
		as3Release: params.As3Release,
		//OverriderCfgMapName:       params.OverriderCfgMapName,
		//l2l3Agent: L2L3Agent{eventChan: params.EventChan,
		//	configWriter: params.ConfigWriter},
		PostManager: NewPostManager(PostParams{
			BIGIPUsername: params.BIGIPUsername,
			BIGIPPassword: params.BIGIPPassword,
			BIGIPURL:      params.BIGIPURL,
			TrustedCerts:  params.TrustedCerts,
			SSLInsecure:   params.SSLInsecure,
			AS3PostDelay:  params.AS3PostDelay,
			LogResponse:   params.LogResponse}),
	}

	//as3Manager.fetchAS3Schema()

	return &as3Manager
}

func (am *AS3Manager) postAS3Declaration(as3Config AS3Config) (bool, string) {

	//am.ResourceRequest = rsReq

	//as3Config := am.as3ActiveConfig
	//as3Config := &AS3Config{}

	// Process Route or Ingress
	//as3Config.resourceConfig = am.prepareAS3ResourceConfig()

	// Process all Configmaps (including overrideAS3)
	//as3Config.configmaps, as3Config.overrideConfigmapData = am.prepareResourceAS3ConfigMaps()

	return am.postAS3Config(as3Config)
}

func (am *AS3Manager) postAS3Config(tempAS3Config AS3Config) (bool, string) {
	unifiedDecl := tempAS3Config.JsonObj

	if DeepEqualJSON(am.as3ActiveConfig.JsonObj, unifiedDecl) {
		return !am.unprocessableEntityStatus, ""
	}

	if am.as3Validation == true {
		if ok := am.validateAS3Template(unifiedDecl); !ok {
			return true, ""
		}
	}

	log.Debugf("[AS3] Posting AS3 Declaration")

	//am.as3ActiveConfig.updateConfig(tempAS3Config)

	var tenants []string = nil
	tenants = append(tenants, "")

	//if am.FilterTenants {
	//	tenants = getTenants(unifiedDecl, true)
	//}

	return am.PostManager.postConfig(unifiedDecl, tenants)
}

// configDeployer blocks on ReqChan
// whenever gets unblocked posts active configuration to BIG-IP
func (am *AS3Manager) ConfigDeployer() {
	// For the very first post after starting controller, need not wait to post
	log.Info("[INFO] running config deployer")
	firstPost := true
	am.unprocessableEntityStatus = false
	for msgReq := range am.ReqChan {
		log.Info("[INFO] received new config")
		if !firstPost && am.PostManager.AS3PostDelay != 0 {
			// Time (in seconds) that CIS waits to post the AS3 declaration to BIG-IP.
			log.Debugf("[AS3] Delaying post to BIG-IP for %v seconds", am.PostManager.AS3PostDelay)
			_ = <-time.After(time.Duration(am.PostManager.AS3PostDelay) * time.Second)
		}

		// After postDelay expires pick up latest declaration, if available
		select {
		case msgReq = <-am.ReqChan:
		case <-time.After(1 * time.Microsecond):
		}

		posted, event := am.postAS3Declaration(msgReq)
		// To handle general errors
		for !posted {
			am.unprocessableEntityStatus = true
			timeout := getTimeDurationForErrorResponse(event)
			log.Debugf("[AS3] Error handling for event %v", event)
			posted, event = am.postOnEventOrTimeout(timeout)
		}
		firstPost = false
		if event == responseStatusOk {
			am.unprocessableEntityStatus = false
			log.Debugf("[AS3] Preparing response message to response handler")
			//am.SendARPEntries()
			//am.SendAgentResponse()
			log.Debugf("[AS3] Sent response message to response handler")
		}
	}
}

// Helper method used by configDeployer to handle error responses received from BIG-IP
func (am *AS3Manager) postOnEventOrTimeout(timeout time.Duration) (bool, string) {
	select {
	case msgReq := <-am.ReqChan:
		return am.postAS3Declaration(msgReq)
	case <-time.After(timeout):
		var tenants []string = nil
		//if am.FilterTenants {
		//	tenants = []{""} //getTenants(am.as3ActiveConfig, true)
		//}
		myJson, err := json.Marshal(am.as3ActiveConfig)
		if err != nil {
			log.Error(err)
		}
		unifiedDeclaration := string(myJson)
		return am.PostManager.postConfig(unifiedDeclaration, tenants)
	}
}

// Method to verify if App Services are installed or CIS as3 version is
// compatible with BIG-IP, it will return with error if any one of the
// requirements are not met
func (am *AS3Manager) IsBigIPAppServicesAvailable() error {
	version, build, err := am.PostManager.GetBigipAS3Version()
	am.as3Version = version
	as3Build := build
	am.as3Release = am.as3Version + "-" + as3Build
	if err != nil {
		log.Errorf("[AS3] %v ", err)
		return err
	}
	versionstr := version[:strings.LastIndex(version, ".")]
	bigIPVersion, err := strconv.ParseFloat(versionstr, 64)
	if err != nil {
		log.Errorf("[AS3] Error while converting AS3 version to float")
		return err
	}
	if bigIPVersion >= as3SupportedVersion {
		log.Debugf("[AS3] BIGIP is serving with AS3 version: %v", version)
		return nil
	}

	return fmt.Errorf("CIS versions >= 2.0 are compatible with AS3 versions >= %v. "+
		"Upgrade AS3 version in BIGIP from %v to %v or above.", as3SupportedVersion,
		bigIPVersion, as3SupportedVersion)
}

func DeepEqualJSON(decl1, decl2 string) bool {
	if decl1 == "" && decl2 == "" {
		return true
	}
	var o1, o2 interface{}

	err := json.Unmarshal([]byte(decl1), &o1)
	if err != nil {
		return false
	}

	err = json.Unmarshal([]byte(decl2), &o2)
	if err != nil {
		return false
	}

	return reflect.DeepEqual(o1, o2)
}

// Validates the AS3 Template
func (am *AS3Manager) validateAS3Template(template string) bool {
	var schemaLoader gojsonschema.JSONLoader
	// Load AS3 Schema
	schemaLoader = gojsonschema.NewReferenceLoader(am.As3SchemaLatest)
	// Load AS3 Template
	documentLoader := gojsonschema.NewStringLoader(template)
	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		log.Errorf("%s", err)
		return false
	}

	if !result.Valid() {
		log.Errorf("[AS3] Template is not valid. see errors")
		for _, desc := range result.Errors() {
			log.Errorf("- %s\n", desc)
		}
		return false
	}

	return true
}
