package vault

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"gitlab.com/signmykey/signmykey/builtin/signer"
)

// Signer struct represents Hashicorp Vault options for signing SSH Key.
type Signer struct {
	Address  string
	Port     int
	UseTLS   bool
	RoleID   string
	SecretID string
	Path     string
	Role     string
	SignTTL  string
	fullAddr string
}

// Init method is used to ingest config of Signer
func (v *Signer) Init(config map[string]string) error {
	neededEntries := []string{
		"vaultAddr",
		"vaultPort",
		"vaultTLS",
		"vaultRoleID",
		"vaultSecretID",
		"vaultPath",
		"vaultRole",
		"vaultSignTTL",
	}

	for _, entry := range neededEntries {
		if _, ok := config[entry]; !ok {
			return fmt.Errorf("Config entry %s missing for Signer", entry)
		}
	}

	// Conversions
	port, err := strconv.Atoi(config["vaultPort"])
	if err != nil {
		return err
	}
	useTLS, err := strconv.ParseBool(config["vaultTLS"])
	if err != nil {
		return err
	}

	v.Address = config["vaultAddr"]
	v.Port = port
	v.UseTLS = useTLS
	v.RoleID = config["vaultRoleID"]
	v.SecretID = config["vaultSecretID"]
	v.Path = config["vaultPath"]
	v.Role = config["vaultRole"]
	v.SignTTL = config["vaultSignTTL"]

	var scheme string
	if v.UseTLS {
		scheme = "https"
	} else {
		scheme = "http"
	}
	v.fullAddr = fmt.Sprintf("%s://%s:%d/v1", scheme, v.Address, v.Port)

	return nil
}

// ReadCA method read CA public cert from Hashicorp Vault backend
func (v Signer) ReadCA() (string, error) {
	token, err := v.getToken()
	if err != nil {
		return "", errors.Wrap(err, "error getting auth token")
	}

	client := &http.Client{}
	req, err := http.NewRequest(
		"GET",
		fmt.Sprintf("%s/%s/config/ca", v.fullAddr, v.Path),
		nil,
	)
	if err != nil {
		return "", errors.Wrap(err, "error creating new httprequest")
	}
	req.Header.Add("X-Vault-Token", token)

	resp, err := client.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "failed during Get request")
	}
	defer resp.Body.Close() // nolint: errcheck

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("unknown error from Vault with status code %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "error reading response body")
	}

	var caResp map[string]interface{}
	err = json.Unmarshal(body, &caResp)
	if err != nil {
		return "", errors.Wrap(err, "error unmarshaling vault response")
	}
	val, ok := caResp["data"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("CA public key not found in Vault response")
	}
	publicKey, ok := val["public_key"].(string)
	if !ok {
		return "", fmt.Errorf("CA public key not found in Vault response")
	}

	// Remove newline
	publicKey = strings.Replace(publicKey, "\n", "", 1)

	return publicKey, nil
}

// Sign method is used to sign passed SSH Key.
func (v Signer) Sign(certreq signer.CertReq) (string, error) {

	err := checkCertReqFields(certreq)
	if err != nil {
		return "", errors.Wrap(err, "certificate request fields checking failed")
	}

	token, err := v.getToken()
	if err != nil {
		return "", errors.Wrap(err, "error getting auth token")
	}

	data, err := json.Marshal(map[string]string{
		"key_id":           certreq.ID,
		"public_key":       certreq.Key,
		"valid_principals": strings.Join(certreq.Principals, ","),
		"ttl":              v.SignTTL,
	})
	if err != nil {
		return "", errors.Wrap(err, "marshaling of sign request payload failed")
	}

	client := &http.Client{}
	req, err := http.NewRequest(
		"POST",
		fmt.Sprintf("%s/%s/sign/%s", v.fullAddr, v.Path, v.Role),
		bytes.NewBuffer(data),
	)
	if err != nil {
		return "", errors.Wrap(err, "error creating new httprequest")
	}
	req.Header.Add("X-Vault-Token", token)

	resp, err := client.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "failed during Post request")
	}
	defer resp.Body.Close() // nolint: errcheck

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("unknown error from Vault with status code %d", resp.StatusCode)
	}

	signedKey, err := extractSignedKey(resp)

	return signedKey, err
}

func extractSignedKey(resp *http.Response) (string, error) {
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "error reading response body")
	}

	var signResp map[string]interface{}
	err = json.Unmarshal(body, &signResp)
	if err != nil {
		return "", errors.Wrap(err, "error unmarshaling Vault response")
	}
	val, ok := signResp["data"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("Signed key not found in Vault response")
	}
	signedKey, ok := val["signed_key"].(string)
	if !ok {
		return "", fmt.Errorf("Signed key not found in Vault response")
	}

	return signedKey, nil
}

func checkCertReqFields(certreq signer.CertReq) error {
	if len(certreq.Principals) == 0 {
		return fmt.Errorf("Empty list of principals")
	}
	if len(certreq.ID) == 0 {
		return fmt.Errorf("Empty ID string")
	}
	if len(certreq.Key) == 0 {
		return fmt.Errorf("Empty key string")
	}

	return nil
}

func (v Signer) getToken() (string, error) {

	data, err := json.Marshal(map[string]string{
		"role_id":   v.RoleID,
		"secret_id": v.SecretID,
	})
	if err != nil {
		return "", err
	}

	resp, err := http.Post(
		fmt.Sprintf("%s/auth/approle/login", v.fullAddr),
		"application/json",
		bytes.NewBuffer(data))

	if err != nil {
		return "", err
	}
	defer resp.Body.Close() // nolint: errcheck

	if resp.StatusCode == 400 {
		return "", fmt.Errorf("invalid role_id or secret_id")
	}
	if resp.StatusCode == 500 {
		return "", fmt.Errorf("Vault internal server error")
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("unknown error during authentication with status code %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var loginResp map[string]interface{}
	err = json.Unmarshal(body, &loginResp)
	if err != nil {
		return "", err
	}
	val, ok := loginResp["auth"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("client token not found in AppRole login response")
	}
	token, ok := val["client_token"].(string)
	if !ok {
		return "", fmt.Errorf("client token not found in AppRole login response")
	}

	return token, nil
}
