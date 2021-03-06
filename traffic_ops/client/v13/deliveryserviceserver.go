/*

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package v13

import (
	"encoding/json"
	"strconv"

	"github.com/apache/trafficcontrol/lib/go-tc"
	"github.com/apache/trafficcontrol/lib/go-util"
)

func (to *Session) GetDeliveryServiceServers() ([]tc.DeliveryServiceServer, ReqInf, error) {
	path := apiBase + `/deliveryserviceserver`
	// deliveryService
	resp := tc.DeliveryServiceServerResponse{}
	reqInf, err := get(to, path, &resp)
	if err != nil {
		return nil, reqInf, err
	}
	return resp.Response, reqInf, nil
}

// CreateDeliveryServiceServers associates the given servers with the given delivery services. If replace is true, it deletes any existing associations for the given delivery service.
func (to *Session) CreateDeliveryServiceServers(dsID int, serverIDs []int, replace bool) (*tc.DSServerIDs, error) {
	path := apiBase + `/deliveryserviceserver`
	req := tc.DSServerIDs{
		DeliveryServiceID: util.IntPtr(dsID),
		ServerIDs:         serverIDs,
		Replace:           util.BoolPtr(replace),
	}
	jsonReq, err := json.Marshal(&req)
	if err != nil {
		return nil, err
	}
	resp := struct {
		Response tc.DSServerIDs `json:"response"`
	}{}
	if err := post(to, path, jsonReq, &resp); err != nil {
		return nil, err
	}
	return &resp.Response, nil
}

// DeleteDeliveryService deletes the given delivery service server association.
func (to *Session) DeleteDeliveryServiceServer(dsID int, serverID int) error {
	path := apiBase + `/deliveryservice_server/` + strconv.Itoa(dsID) + `/` + strconv.Itoa(serverID)
	resp := struct{ tc.Alerts }{}
	err := del(to, path, &resp)
	if err != nil {
		return err
	}
	return nil
}
