package docker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/spf13/viper"
)

//Prepare populate image.FSLayers with the layer from manifest coming from `docker save` command. Layer.History will be populated with `docker history` command
func Prepare(im *Image) error {
	imageName := im.Name + ":" + im.Tag
	logrus.Debugf("preparing %v", imageName)

	path, err := save(imageName)
	// defer os.RemoveAll(path)
	if err != nil {
		return fmt.Errorf("could not save image: %s", err)
	}

	// Retrieve history.
	logrus.Infoln("Getting image's history")
	manifestLayerIDs, err := historyFromManifest(path)

	historyLayerIDs, err := historyFromCommand(imageName)

	if err != nil || (len(manifestLayerIDs) == 0 && len(historyLayerIDs) == 0) {
		return fmt.Errorf("Could not get image's history: %s", err)
	}

	for i, l := range manifestLayerIDs {
		im.FsLayers = append(im.FsLayers, Layer{BlobSum: l, History: historyLayerIDs[i]})
	}

	return nil
}

//FromHistory populate image.FSLayers with the layer from `docker history` command
func FromHistory(im *Image) error {
	imageName := im.Name + ":" + im.Tag
	layerIDs, err := historyFromCommand(imageName)

	if err != nil || len(layerIDs) == 0 {
		return fmt.Errorf("Could not get image's history: %s", err)
	}

	for _, l := range layerIDs {
		im.FsLayers = append(im.FsLayers, Layer{BlobSum: l})
	}

	return nil
}

//Docker0InterfaceIP return the docker0 interface ip by running `ip route show | grep docker0 | awk {print $9}`
func Docker0InterfaceIP() (string, error) {
	var localIP bytes.Buffer

	ip := exec.Command("ip", "route", "show")
	rGrep, wIP := io.Pipe()
	grep := exec.Command("grep", "docker0")
	ip.Stdout = wIP
	grep.Stdin = rGrep
	awk := exec.Command("awk", "{print $9}")
	rAwk, wGrep := io.Pipe()
	grep.Stdout = wGrep
	awk.Stdin = rAwk
	awk.Stdout = &localIP
	err := ip.Start()
	if err != nil {
		return "", err
	}
	err = grep.Start()
	if err != nil {
		return "", err
	}
	err = awk.Start()
	if err != nil {
		return "", err
	}
	err = ip.Wait()
	if err != nil {
		return "", err
	}
	err = wIP.Close()
	if err != nil {
		return "", err
	}
	err = grep.Wait()
	if err != nil {
		return "", err
	}
	err = wGrep.Close()
	if err != nil {
		return "", err
	}
	err = awk.Wait()
	if err != nil {
		return "", err
	}
	return localIP.String(), nil
}

//LocalServerIP return the local hyperclair server IP
func LocalServerIP() (string, error) {
	localPort := viper.GetString("hyperclair.local.port")
	localIP := viper.GetString("hyperclair.local.ip")
	if localIP == "" {
		logrus.Infoln("retrieving docker0 interface as local IP")
		var err error
		localIP, err = Docker0InterfaceIP()
		if err != nil {
			return "", fmt.Errorf("retrieving docker0 interface ip: %v", err)
		}
	}
	return strings.TrimSpace(localIP) + ":" + localPort, nil
}

func cleanLocal() error {
	logrus.Debugln("cleaning temporary local repository")
	err := os.RemoveAll(TmpLocal())

	if err != nil {
		return fmt.Errorf("cleaning temporary local repository: %v", err)
	}

	return nil
}

func save(imageName string) (string, error) {
	path := TmpLocal() + "/" + strings.Split(imageName, ":")[0] + "/blobs"

	if _, err := os.Stat(path); os.IsExist(err) {
		err := os.RemoveAll(path)
		if err != nil {
			return "", err
		}
	}

	err := os.MkdirAll(path, 0755)
	if err != nil {
		return "", err
	}

	var stderr bytes.Buffer
	logrus.Debugln("docker image to save: ", imageName)
	logrus.Debugln("saving in: ", path)
	save := exec.Command("docker", "save", imageName)
	save.Stderr = &stderr
	extract := exec.Command("tar", "xf", "-", "-C"+path)
	extract.Stderr = &stderr
	pipe, err := extract.StdinPipe()
	if err != nil {
		return "", err
	}
	save.Stdout = pipe

	err = extract.Start()
	if err != nil {
		return "", errors.New(stderr.String())
	}
	err = save.Run()
	if err != nil {
		return "", errors.New(stderr.String())
	}
	err = pipe.Close()
	if err != nil {
		return "", err
	}
	err = extract.Wait()
	if err != nil {
		return "", errors.New(stderr.String())
	}
	return path, nil
}

func historyFromManifest(path string) ([]string, error) {
	mf, err := os.Open(path + "/manifest.json")
	if err != nil {
		return nil, err
	}
	defer mf.Close()

	// https://github.com/docker/docker/blob/master/image/tarexport/tarexport.go#L17
	type manifestItem struct {
		Config   string
		RepoTags []string
		Layers   []string
	}

	var manifest []manifestItem
	if err = json.NewDecoder(mf).Decode(&manifest); err != nil {
		return nil, err
	} else if len(manifest) != 1 {
		return nil, err
	}
	var layers []string
	for _, layer := range manifest[0].Layers {
		layers = append(layers, strings.TrimSuffix(layer, "/layer.tar"))
	}
	return layers, nil
}

func historyFromCommand(imageName string) ([]string, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("docker", "history", "-q", "--no-trunc", imageName)
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return []string{}, err
	}

	err = cmd.Start()
	if err != nil {
		return []string{}, errors.New(stderr.String())
	}

	var layers []string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		layers = append(layers, scanner.Text())
	}

	for i := len(layers)/2 - 1; i >= 0; i-- {
		opp := len(layers) - 1 - i
		layers[i], layers[opp] = layers[opp], layers[i]
	}

	return layers, nil
}
