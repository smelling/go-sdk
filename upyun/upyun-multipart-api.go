package upyun

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// UPYUN MultiPart Upload API
type UpYunMultiPart struct {
	upYunHTTPCore

	Bucket    string
	APIKey    string
	BlockSize int64
}

// upload response body
type UploadResp struct {
	SaveToken string `json:"save_token"`
	Secret    string `json:"token_secret"`
	Bucket    string `json:"bucket_name"`
	Blocks    int    `json:"blocks"`
	Status    []int  `json:"status"`
	ExpireAt  int64  `json:"expire_at"`
}

// merge response body
type MergeResp struct {
	Path          string `json:"path"`
	ContentType   string `json:"mimetype"`
	ContentLength string `json:"file_size"`
	LastModify    int64  `json:"last_modified"`
	Signature     string `json:"signature"`
	ImageWidth    int    `json:"image_width"`
	ImageHeight   int    `json:"image_height"`
	ImageFrames   int    `json:"image_frames"`
}

func NewUpYunMultiPart(bucket, apikey string, blocksize int64) *UpYunMultiPart {
	up := &UpYunMultiPart{
		APIKey:    apikey,
		Bucket:    bucket,
		BlockSize: blocksize,
	}

	up.endpoint = "m0.api.upyun.com"
	up.httpClient = &http.Client{
		Transport: &http.Transport{
			Dial: timeoutDialer(defaultConnectTimeout),
		},
	}

	return up
}

// make multipart upload authorization
func (ump *UpYunMultiPart) makeMPAuth(secret string, kwargs map[string]interface{}) string {
	var keys []string
	for k, _ := range kwargs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	sign := ""
	for _, k := range keys {
		sign += k + fmt.Sprint(kwargs[k])
	}

	return md5Str(sign + secret)
}

func (ump *UpYunMultiPart) makePolicy(kwargs map[string]interface{}) (string, error) {
	data, err := json.Marshal(kwargs)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

func (ump *UpYunMultiPart) InitUpload(key string, value *os.File,
	expire int64, options map[string]interface{}) ([]byte, error) {
	// seek at start point
	value.Seek(0, 0)
	hash, fsize, err := md5sum(value)
	if err != nil {
		return nil, err
	}

	opt := map[string]interface{}{
		"path":        key,
		"expiration":  time.Now().Unix() + expire,
		"file_hash":   string(hash),
		"file_size":   fsize,
		"file_blocks": (fsize + ump.BlockSize - 1) / ump.BlockSize,
	}
	if options != nil {
		for k, v := range options {
			opt[k] = v
		}
	}

	// make policy
	policy, err := ump.makePolicy(opt)
	if err != nil {
		return nil, err
	}

	// make signature
	signature := ump.makeMPAuth(ump.APIKey, opt)
	payload := fmt.Sprintf("policy=%s&signature=%s", policy, signature)

	// set http headers
	headers := map[string]string{
		"Content-Length": fmt.Sprint(len(payload)),
		"Content-Type":   "application/x-www-form-urlencoded",
	}

	url := fmt.Sprintf("http://%s/%s/", ump.endpoint, ump.Bucket)
	resp, err := ump.doHTTPRequest("POST",
		url, headers, strings.NewReader(payload))

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if resp.StatusCode/100 == 2 {
		return body, err
	}

	return nil, newRespError(string(body), resp.Header)
}

func (ump *UpYunMultiPart) UploadBlock(fd *os.File, bindex int, expire int64,
	fpath, saveToken, secret string) ([]byte, error) {

	block := make([]byte, ump.BlockSize)
	// seek to this block's start point
	_, err := fd.Seek(ump.BlockSize*int64(bindex), 0)
	if err != nil {
		return nil, err
	}

	// read block
	n, err := fd.Read(block)
	if err != nil {
		return nil, err
	}
	rblock := block[:n]

	// calculate md5
	hash, _, err := md5sum(bytes.NewBuffer(rblock))
	if err != nil {
		return nil, err
	}

	opts := map[string]interface{}{
		"save_token":  saveToken,
		"expiration":  fmt.Sprint(expire),
		"block_index": bindex,
		"block_hash":  string(hash),
	}

	policy, err := ump.makePolicy(opts)
	if err != nil {
		return nil, err
	}

	signature := ump.makeMPAuth(secret, opts)
	url := fmt.Sprintf("http://%s/%s/", ump.endpoint, ump.Bucket)

	resp, err := ump.doFormRequest(url, policy, signature, fpath, bytes.NewBuffer(rblock))
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if resp.StatusCode/100 == 2 {
		return body, err
	}

	return nil, newRespError(string(body), resp.Header)
}

func (ump *UpYunMultiPart) MergeBlock(saveToken, secret string,
	expire int64) ([]byte, error) {
	opts := map[string]interface{}{
		"save_token": saveToken,
		"expiration": fmt.Sprint(expire),
	}

	policy, err := ump.makePolicy(opts)
	if err != nil {
		return nil, err
	}

	signature := ump.makeMPAuth(secret, opts)
	payload := fmt.Sprintf("policy=%s&signature=%s", policy, signature)

	headers := map[string]string{
		"Content-Length": fmt.Sprint(len(payload)),
		"Content-Type":   "application/x-www-form-urlencoded",
	}

	url := fmt.Sprintf("http://%s/%s/", ump.endpoint, ump.Bucket)
	resp, err := ump.doHTTPRequest("POST",
		url, headers, strings.NewReader(payload))

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if resp.StatusCode/100 == 2 {
		return body, err
	}

	return nil, newRespError(string(body), resp.Header)
}

// TODO: support io.reader
func (ump *UpYunMultiPart) Put(key, fpath string,
	expire int64, options map[string]interface{}) (*MergeResp, error) {
	fd, err := os.Open(fpath)
	if err != nil {
		return nil, err
	}

	rdata, err := ump.InitUpload(key, fd, expire, options)
	if err != nil {
		return nil, errors.New("failed to init upload." + err.Error())
	}

	var ub UploadResp
	if err := json.Unmarshal(rdata, &ub); err != nil {
		return nil, err
	}

	saveToken := ub.SaveToken
	secret := ub.Secret
	status := ub.Status
	for try := 1; try <= 3; try++ {
		ok := 0
		for idx, _ := range status {
			if status[idx] == 0 {
				_, err := ump.UploadBlock(fd, idx, expire, fpath, saveToken, secret)
				if err != nil {
					break
				}
				status[idx] = 1
			}
			ok++
		}

		if ok == len(status) {
			break
		}

		if try == 3 {
			return nil, errors.New("failed to upload block." + err.Error())
		}
	}

	data, err := ump.MergeBlock(saveToken, secret, expire)
	if err != nil {
		return nil, errors.New("failed to merge blocks." + err.Error())
	}

	var mr MergeResp
	if err := json.Unmarshal(data, &mr); err != nil {
		return nil, err
	}

	return &mr, nil
}
