package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"go.coder.com/cli"
	"go.coder.com/flog"

	"cdr.dev/coder-cli/internal/config"
	"cdr.dev/coder-cli/internal/entclient"
)

var (
	privateKeyFilepath = filepath.Join(os.Getenv("HOME"), ".ssh", "coder_enterprise")
)

type configSSHCmd struct {
	filepath string
	remove   bool

	startToken, startMessage, endToken string
}

func (cmd *configSSHCmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "config-ssh",
		Usage: "",
		Desc:  "add your Coder Enterprise environments to ~/.ssh/config",
	}
}

func (cmd *configSSHCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&cmd.remove, "remove", false, "remove the auto-generated Coder Enterprise ssh config")
	home := os.Getenv("HOME")
	defaultPath := filepath.Join(home, ".ssh", "config")
	fl.StringVar(&cmd.filepath, "config-path", defaultPath, "overide the default path of your ssh config file")

	cmd.startToken = "# ------------START-CODER-ENTERPRISE-----------"
	cmd.startMessage = `# The following has been auto-generated by "coder config-ssh"
# to make accessing your Coder Enterprise environments easier.
#
# To remove this blob, run:
#
#    coder config-ssh --remove
#
# You should not hand-edit this section, unless you are deleting it.`
	cmd.endToken = "# ------------END-CODER-ENTERPRISE------------"
}

func (cmd *configSSHCmd) Run(fl *pflag.FlagSet) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	currentConfig, err := readStr(cmd.filepath)
	if os.IsNotExist(err) {
		// SSH configs are not always already there.
		currentConfig = ""
	} else if err != nil {
		flog.Fatal("failed to read ssh config file %q: %v", cmd.filepath, err)
	}

	startIndex := strings.Index(currentConfig, cmd.startToken)
	endIndex := strings.Index(currentConfig, cmd.endToken)

	if cmd.remove {
		if startIndex == -1 || endIndex == -1 {
			flog.Fatal("the Coder Enterprise ssh configuration section could not be safely deleted or does not exist")
		}
		currentConfig = currentConfig[:startIndex-1] + currentConfig[endIndex+len(cmd.endToken)+1:]

		err = writeStr(cmd.filepath, currentConfig)
		if err != nil {
			flog.Fatal("failed to write to ssh config file %q: %v", cmd.filepath, err)
		}

		return
	}

	entClient := requireAuth()

	sshAvailable := cmd.ensureSSHAvailable(ctx)
	if !sshAvailable {
		flog.Fatal("SSH is disabled or not available for your Coder Enterprise deployment.")
	}

	me, err := entClient.Me()
	if err != nil {
		flog.Fatal("failed to fetch username: %v", err)
	}

	envs := getEnvs(entClient)
	if len(envs) < 1 {
		flog.Fatal("no environments found")
	}
	newConfig, err := cmd.makeNewConfigs(me.Username, envs)
	if err != nil {
		flog.Fatal("failed to make new ssh configurations: %v", err)
	}

	// if we find the old config, remove those chars from the string
	if startIndex != -1 && endIndex != -1 {
		currentConfig = currentConfig[:startIndex-1] + currentConfig[endIndex+len(cmd.endToken)+1:]
	}

	err = writeStr(cmd.filepath, currentConfig+newConfig)
	if err != nil {
		flog.Fatal("failed to write new configurations to ssh config file %q: %v", cmd.filepath, err)
	}
	err = writeSSHKey(ctx, entClient)
	if err != nil {
		flog.Fatal("failed to fetch and write ssh key: %v", err)
	}

	fmt.Printf("An auto-generated ssh config was written to %q\n", cmd.filepath)
	fmt.Printf("Your private ssh key was written to %q\n", privateKeyFilepath)
	fmt.Println("You should now be able to ssh into your environment")
	fmt.Printf("For example, try running\n\n\t$ ssh coder.%s\n\n", envs[0].Name)
}

func writeSSHKey(ctx context.Context, client *entclient.Client) error {
	key, err := client.SSHKey()
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(privateKeyFilepath, []byte(key.PrivateKey), 0400)
	if err != nil {
		return err
	}
	return nil
}

func (cmd *configSSHCmd) makeNewConfigs(userName string, envs []entclient.Environment) (string, error) {
	hostname, err := configuredHostname()
	if err != nil {
		return "", nil
	}

	newConfig := fmt.Sprintf("\n%s\n%s\n\n", cmd.startToken, cmd.startMessage)
	for _, env := range envs {
		newConfig += cmd.makeConfig(hostname, userName, env.Name)
	}
	newConfig += fmt.Sprintf("\n%s\n", cmd.endToken)

	return newConfig, nil
}

func (cmd *configSSHCmd) makeConfig(host, userName, envName string) string {
	return fmt.Sprintf(
		`Host coder.%s
    HostName %s
    User %s-%s
    StrictHostKeyChecking no
    ConnectTimeout=0
    IdentityFile=%s
    ServerAliveInterval 60
    ServerAliveCountMax 3
`, envName, host, userName, envName, privateKeyFilepath)
}

func (cmd *configSSHCmd) ensureSSHAvailable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	host, err := configuredHostname()
	if err != nil {
		return false
	}

	var dialer net.Dialer
	_, err = dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "22"))
	return err == nil
}

func configuredHostname() (string, error) {
	u, err := config.URL.Read()
	if err != nil {
		return "", err
	}
	url, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	return url.Hostname(), nil
}

func writeStr(filename, data string) error {
	return ioutil.WriteFile(filename, []byte(data), 0777)
}

func readStr(filename string) (string, error) {
	contents, err := ioutil.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return string(contents), nil
}
