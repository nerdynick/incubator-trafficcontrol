package cdn

/*
 * Licensed to the Apache Software Foundation (ASF) under one
 * or more contributor license agreements.  See the NOTICE file
 * distributed with this work for additional information
 * regarding copyright ownership.  The ASF licenses this file
 * to you under the Apache License, Version 2.0 (the
 * "License"); you may not use this file except in compliance
 * with the License.  You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/apache/trafficcontrol/lib/go-log"
	"github.com/apache/trafficcontrol/lib/go-tc"
	"github.com/apache/trafficcontrol/lib/go-util"
	"github.com/apache/trafficcontrol/traffic_ops/traffic_ops_golang/api"
	"github.com/apache/trafficcontrol/traffic_ops/traffic_ops_golang/config"
	"github.com/apache/trafficcontrol/traffic_ops/traffic_ops_golang/deliveryservice"
	"github.com/apache/trafficcontrol/traffic_ops/traffic_ops_golang/riaksvc"
)

const CDNDNSSECKeyType = "dnssec"
const DNSSECStatusExisting = "existing"

func CreateDNSSECKeys(w http.ResponseWriter, r *http.Request) {
	inf, userErr, sysErr, errCode := api.NewInfo(r, nil, nil)
	if userErr != nil || sysErr != nil {
		api.HandleErr(w, r, errCode, userErr, sysErr)
		return
	}
	defer inf.Close()

	req := tc.CDNDNSSECGenerateReq{}
	if err := api.Parse(r.Body, inf.Tx.Tx, &req); err != nil {
		api.HandleErr(w, r, http.StatusBadRequest, errors.New("parsing request: "+err.Error()), nil)
		return
	}
	if req.EffectiveDateUnix == nil {
		req.EffectiveDateUnix = util.Int64Ptr(time.Now().Unix())
	}
	cdnName := *req.Key
	if err := generateStoreDNSSECKeys(inf.Tx.Tx, inf.Config, cdnName, *req.TTL, *req.KSKExpirationDays, *req.ZSKExpirationDays, *req.EffectiveDateUnix); err != nil {
		api.HandleErr(w, r, http.StatusInternalServerError, nil, errors.New("generating and storing DNSSEC CDN keys: "+err.Error()))
		return
	}
	*inf.CommitTx = true
	api.WriteResp(w, r, "Successfully created dnssec keys for "+cdnName)
}

func generateStoreDNSSECKeys(
	tx *sql.Tx,
	cfg *config.Config,
	cdnName string,
	ttlSeconds uint64,
	kExpDays uint64,
	zExpDays uint64,
	effectiveDateUnix int64,
) error {

	zExp := time.Duration(zExpDays) * time.Hour * 24
	kExp := time.Duration(kExpDays) * time.Hour * 24
	ttl := time.Duration(ttlSeconds) * time.Second

	newKeys := tc.DNSSECKeys{}
	// (tc.DNSSECKeys, bool, error) {
	oldKeys, oldKeysExist, err := riaksvc.GetDNSSECKeys(cdnName, tx, cfg.RiakAuthOptions)
	if err != nil {
		return errors.New("getting old dnssec keys: " + err.Error())
	}
	if oldKeysExist {
		oldKeyCDN, oldKeyCDNExists := oldKeys[cdnName]
		if oldKeyCDNExists && len(oldKeyCDN.KSK) > 0 {
			ksk := oldKeyCDN.KSK[0]
			ksk.Status = DNSSECStatusExisting
			ksk.TTLSeconds = uint64(ttl / time.Second)
			ksk.ExpirationDateUnix = effectiveDateUnix
			oldKeyCDN.KSK = append(oldKeyCDN.KSK, ksk)
		}
		if oldKeyCDNExists && len(oldKeyCDN.ZSK) > 0 {
			zsk := oldKeyCDN.ZSK[0]
			zsk.Status = DNSSECStatusExisting
			zsk.TTLSeconds = uint64(ttl / time.Second)
			zsk.ExpirationDateUnix = effectiveDateUnix
			oldKeyCDN.ZSK = append(oldKeyCDN.ZSK, zsk)
		}
		newKeys[cdnName] = tc.DNSSECKeySet{oldKeyCDN.ZSK, oldKeyCDN.KSK}
	} else {
		// TODO create CDN keys if they don't exist?
		return errors.New("getting DNSSec keys from Riak: no DNSSec keys for CDN")
	}
	cdnKeys := newKeys[cdnName]
	dses, cdnDomain, err := getCDNDeliveryServices(tx, cdnName)
	if err != nil {
		return errors.New("getting cdn delivery services: " + err.Error())
	}
	dsNames := []string{}
	for _, ds := range dses {
		dsNames = append(dsNames, ds.Name)
	}
	matchLists, err := deliveryservice.GetDeliveryServicesMatchLists(dsNames, tx)
	if err != nil {
		return errors.New("getting delivery service matchlists: " + err.Error())
	}
	for _, ds := range dses {
		if !ds.Type.IsHTTP() && !ds.Type.IsDNS() {
			continue // skip delivery services that aren't DNS or HTTP (e.g. ANY_MAP)
		}

		matchlist, ok := matchLists[ds.Name]
		if !ok {
			return errors.New("no regex match list found for delivery service '" + ds.Name)
		}

		exampleURLs := deliveryservice.MakeExampleURLs(ds.Protocol, ds.Type, ds.RoutingName, matchlist, cdnDomain)
		log.Infoln("Creating keys for " + ds.Name)
		overrideTTL := true
		dsKeys, err := deliveryservice.CreateDNSSECKeys(tx, cfg, ds.Name, exampleURLs, cdnKeys, kExp, zExp, ttl, overrideTTL)
		if err != nil {
			return errors.New("creating delivery service DNSSEC keys: " + err.Error())
		}
		newKeys[ds.Name] = dsKeys
	}
	if err := riaksvc.PutDNSSECKeys(newKeys, cdnName, tx, cfg.RiakAuthOptions); err != nil {
		return errors.New("putting Riak DNSSEC CDN keys: " + err.Error())
	}
	return nil
}

type CDNDS struct {
	Name        string
	Protocol    *int
	Type        tc.DSType
	RoutingName string
}

// getCDNDeliveryServices returns basic data for the delivery services on the given CDN, as well as the CDN name, or any error.
func getCDNDeliveryServices(tx *sql.Tx, cdn string) ([]CDNDS, string, error) {
	q := `
SELECT ds.xml_id, ds.protocol, t.name as type, ds.routing_name, cdn.domain_name as cdn_domain
FROM deliveryservice as ds
JOIN cdn ON ds.cdn_id = cdn.id
JOIN type as t ON ds.type = t.id
WHERE cdn.name = $1
`
	rows, err := tx.Query(q, cdn)
	if err != nil {
		return nil, "", errors.New("getting cdn delivery services: " + err.Error())
	}
	defer rows.Close()
	cdnDomain := ""
	dses := []CDNDS{}
	for rows.Next() {
		ds := CDNDS{}
		dsTypeStr := ""
		if err := rows.Scan(&ds.Name, &ds.Protocol, dsTypeStr, &ds.RoutingName, &cdnDomain); err != nil {
			return nil, "", errors.New("scanning cdn delivery services: " + err.Error())
		}
		dsType := tc.DSTypeFromString(dsTypeStr)
		if dsType == tc.DSTypeInvalid {
			return nil, "", errors.New("got invalid delivery service type '" + dsTypeStr + "'")
		}
		ds.Type = dsType
		dses = append(dses, ds)
	}
	return dses, cdnDomain, nil
}

func DeleteDNSSECKeys(w http.ResponseWriter, r *http.Request) {
	inf, userErr, sysErr, errCode := api.NewInfo(r, []string{"name"}, nil)
	if userErr != nil || sysErr != nil {
		api.HandleErr(w, r, errCode, userErr, sysErr)
		return
	}
	defer inf.Close()

	key := inf.Params["name"]

	riakCluster, err := riaksvc.GetRiakClusterTx(inf.Tx.Tx, inf.Config.RiakAuthOptions)
	if err != nil {
		api.HandleErr(w, r, http.StatusInternalServerError, nil, errors.New("getting riak cluster: "+err.Error()))
		return
	}
	if err := riakCluster.Start(); err != nil {
		api.HandleErr(w, r, http.StatusInternalServerError, nil, errors.New("starting riak cluster: "+err.Error()))
		return
	}
	defer riaksvc.StopCluster(riakCluster)

	if err := riaksvc.DeleteObject(key, CDNDNSSECKeyType, riakCluster); err != nil {
		api.HandleErr(w, r, http.StatusInternalServerError, nil, errors.New("deleting cdn dnssec keys: "+err.Error()))
		return
	}
	*inf.CommitTx = true
	api.CreateChangeLogRawTx(api.ApiChange, "Deleted DNSSEC keys for CDN "+key, inf.User, inf.Tx.Tx)
	api.WriteResp(w, r, "Successfully deleted "+CDNDNSSECKeyType+" for "+key)
}
