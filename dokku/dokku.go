package dokku

import (
	"fmt"
	"log"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/melbahja/goph"
)

//
type DokkuApp struct {
	Id         string
	Name       string
	Locked     bool
	ConfigVars map[string]string
	Domains    []string
}

//
func (app *DokkuApp) setOnResourceData(d *schema.ResourceData) {
	d.SetId(app.Id)
	d.Set("name", app.Name)
	d.Set("locked", app.Locked)

	d.Set("config_vars", app.managedConfigVars(d))

	d.Set("domains", app.Domains)
}

// Leave alone config vars that are set outside of terraform. This is one way
// to avoid vars that are set by dokku etc (e.g DOKKU_PROXY_PORT).
func (app *DokkuApp) managedConfigVars(d *schema.ResourceData) map[string]string {
	tfConfigKeyLookup := make(map[string]struct{})
	tfConfigVars := make(map[string]string)

	// Extract the keys that exist in d
	if c, ok := d.GetOk("config_vars"); ok {
		m := c.(map[string]interface{})
		for k := range m {
			tfConfigKeyLookup[k] = struct{}{}
		}
	}

	for varKey, varVal := range app.ConfigVars {
		if _, ok := tfConfigKeyLookup[varKey]; ok {
			tfConfigVars[varKey] = varVal
		}
	}

	return tfConfigVars
}

// TODO escape quotes
func (app *DokkuApp) configVarsStr() string {
	str := ""
	for k, v := range app.ConfigVars {
		if len(str) > 0 {
			str = str + " "
		}
		str = str + k + "=" + v
	}
	return str
}

func NewDokkuAppFromResourceData(d *schema.ResourceData) *DokkuApp {
	domains := interfaceSliceToStrSlice(d.Get("domains").(*schema.Set).List())

	configVars := make(map[string]string)
	for ck, cv := range d.Get("config_vars").(map[string]interface{}) {
		configVars[ck] = cv.(string)
	}

	return &DokkuApp{
		Name:       d.Get("name").(string),
		Locked:     d.Get("locked").(bool),
		ConfigVars: configVars,
		Domains:    domains,
	}
}

//
func dokkuAppRetrieve(appName string, client *goph.Client) (*DokkuApp, error) {
	readOutput, err := client.Run(fmt.Sprintf("apps:exists %s", appName))
	log.Printf("[DEBUG] apps:exists %v\n", string(readOutput))
	log.Printf("[DEBUG] error from apps:exists %v\n", err)

	app := &DokkuApp{Id: appName, Name: appName, Locked: false}

	if err != nil {
		if err.Error() == "Process exited with status 20" {
			// App does not exist
			app.Id = ""
			log.Printf("[DEBUG] app %s does not exist\n", appName)
			// return nil, err
			return app, nil
		} else {
			return nil, err
		}
	}

	app.ConfigVars = readAppConfig(appName, client)
	domains, err := readAppDomains(appName, client)
	if err != nil {
		return nil, err
	}
	app.Domains = domains

	// ssl, err := readAppSsl(appName, client)
	// if err != nil {
	// 	return nil, err
	// }
	// app.Ssl = ssl

	return app, nil
}

// TODO error handling
func readAppConfig(appName string, sshClient *goph.Client) map[string]string {
	configOutput, _ := sshClient.Run(fmt.Sprintf("config:show %s", appName))

	// if err {
	// 	// TODO
	// }

	configLines := strings.Split(string(configOutput), "\n")

	// TODO validate first line of output

	keyPairs := configLines[1:]

	config := make(map[string]string)

	for _, kp := range keyPairs {
		kp = strings.TrimSpace(kp)
		if len(kp) > 0 {
			parts := strings.Split(kp, ":")
			configKey := parts[0]
			configValue := strings.TrimSpace(parts[1])

			config[configKey] = configValue
		}
	}

	return config
}

//
func readAppDomains(appName string, client *goph.Client) ([]string, error) {
	domainsOutput, err := client.Run(fmt.Sprintf("domains:report %s", appName))

	if err != nil {
		return nil, err
	}

	domainLines := strings.Split(string(domainsOutput), "\n")[1:]

	for _, line := range domainLines {
		parts := strings.Split(line, ":")

		key := strings.TrimSpace(parts[0])

		if key == "Domains app vhosts" {
			domainList := strings.TrimSpace(parts[1])
			if domainList == "" {
				return []string{}, nil
			} else {
				return strings.Split(domainList, " "), nil
			}
		}
	}

	// TODO proper error handling
	return nil, nil
}

//
func dokkuAppCreate(app *DokkuApp, client *goph.Client) error {
	createOutput, err := client.Run(fmt.Sprintf("apps:create %s", app.Name))

	log.Printf("[DEBUG] apps:create %v\n", string(createOutput))

	if err != nil {
		return err
	}

	err = dokkuAppConfigVarsSet(app, client)

	if err != nil {
		return err
	}

	err = dokkuAppDomainsAdd(app, client)

	if err != nil {
		return err
	}

	return nil
}

//
func dokkuAppConfigVarsSet(app *DokkuApp, client *goph.Client) error {
	configVarStr := app.configVarsStr()
	if len(configVarStr) == 0 {
		return nil
	}

	_, err := client.Run(fmt.Sprintf("config:set %s %s", app.Name, configVarStr))
	return err
}

//
func dokkuAppConfigVarsUnset(app *DokkuApp, varsToUnset []string, client *goph.Client) error {
	if len(varsToUnset) == 0 {
		return nil
	}
	log.Printf("[DEBUG] Unsetting keys %v\n", varsToUnset)
	_, err := client.Run(fmt.Sprintf("config:unset %s", strings.Join(varsToUnset, " ")))
	return err
}

//
func dokkuAppDomainsAdd(app *DokkuApp, client *goph.Client) error {
	domainStr := strings.Join(app.Domains, " ")
	_, err := client.Run(fmt.Sprintf("domains:set %s %s", app.Name, domainStr))
	return err
}

//
func dokkuAppUpdate(app *DokkuApp, d *schema.ResourceData, client *goph.Client) error {
	if d.HasChange("name") {
		old, _ := d.GetChange("name")
		renameOutput, err := client.Run(fmt.Sprintf("apps:rename %s %s", old.(string), d.Get("name")))
		log.Printf("[DEBUG] apps:rename %s %s : %v\n", old.(string), d.Get("name"), renameOutput)
		if err != nil {
			return err
		}
	}

	appName := d.Get("name")

	if d.HasChange("config_vars") {
		log.Println("[DEBUG] Changing config keys...")

		oldConfigVarsI, newConfigVarsI := d.GetChange("config_vars")
		oldConfigVars := mapOfInterfacesToMapOfStrings(oldConfigVarsI.(map[string]interface{}))
		newConfigVar := mapOfInterfacesToMapOfStrings(newConfigVarsI.(map[string]interface{}))

		keysToDelete := calculateMissingKeys(newConfigVar, oldConfigVars)

		dokkuAppConfigVarsUnset(app, keysToDelete, client)

		// TODO shouldn't need to duplicate below we already have config set function
		// This is basically an upsert, and will update values even if they haven't changed
		keysToUpsert := make([]string, len(newConfigVar))
		upsertParts := make([]string, len(newConfigVar))
		for newK, newV := range newConfigVar {
			keysToUpsert = append(keysToUpsert, newK)
			upsertParts = append(upsertParts, fmt.Sprintf("%s=%s", newK, newV))
		}

		log.Printf("[DEBUG] Setting keys %v\n", keysToUpsert)
		_, err := client.Run(fmt.Sprintf("config:set %s %s", appName, strings.Join(upsertParts, " ")))

		if err != nil {
			return err
		}
	}

	if d.HasChange("domains") {
		oldDomainsI, newDomainsI := d.GetChange("domains")
		oldDomains := interfaceSliceToStrSlice(oldDomainsI.(*schema.Set).List())
		newDomains := interfaceSliceToStrSlice(newDomainsI.(*schema.Set).List())
		domainsToRemove := calculateMissingStrings(newDomains, oldDomains)

		// Remove domains
		_, err := client.Run(fmt.Sprintf("domains:remove %s %s", appName, strings.Join(domainsToRemove, " ")))

		if err != nil {
			return err
		}

		// Add domains
		_, err = client.Run(fmt.Sprintf("domains:add %s %s", appName, strings.Join(newDomains, " ")))

		if err != nil {
			return err
		}
	}

	return nil
}
