package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cdr.dev/coder-cli/coder-sdk"
	"cdr.dev/coder-cli/pkg/clog"
	"golang.org/x/xerrors"
)

// Helpers for working with the Coder Enterprise API.

// lookupUserOrgs gets a list of orgs the user is apart of.
func lookupUserOrgs(user *coder.User, orgs []coder.Organization) []coder.Organization {
	// NOTE: We don't know in advance how many orgs the user is in so we can't pre-alloc.
	var userOrgs []coder.Organization

	for _, org := range orgs {
		for _, member := range org.Members {
			if member.ID != user.ID {
				continue
			}
			// If we found the user in the org, add it to the list and skip to the next org.
			userOrgs = append(userOrgs, org)
			break
		}
	}
	return userOrgs
}

// getEnvs returns all environments for the user.
func getEnvs(ctx context.Context, client *coder.Client, email string) ([]coder.Environment, error) {
	user, err := client.UserByEmail(ctx, email)
	if err != nil {
		return nil, xerrors.Errorf("get user: %w", err)
	}

	orgs, err := client.Organizations(ctx)
	if err != nil {
		return nil, xerrors.Errorf("get orgs: %w", err)
	}

	orgs = lookupUserOrgs(user, orgs)

	// NOTE: We don't know in advance how many envs we have so we can't pre-alloc.
	var allEnvs []coder.Environment

	for _, org := range orgs {
		envs, err := client.UserEnvironmentsByOrganization(ctx, user.ID, org.ID)
		if err != nil {
			return nil, xerrors.Errorf("get envs for %s: %w", org.Name, err)
		}

		allEnvs = append(allEnvs, envs...)
	}
	return allEnvs, nil
}

// searchForEnv searches a user's environments to find the specified envName. If none is found, the haystack of
// environment names is returned.
func searchForEnv(ctx context.Context, client *coder.Client, envName, userEmail string) (_ *coder.Environment, haystack []string, _ error) {
	envs, err := getEnvs(ctx, client, userEmail)
	if err != nil {
		return nil, nil, xerrors.Errorf("get environments: %w", err)
	}

	// NOTE: We don't know in advance where we will find the env, so we can't pre-alloc.
	for _, env := range envs {
		if env.Name == envName {
			return &env, nil, nil
		}
		// Keep track of what we found for the logs.
		haystack = append(haystack, env.Name)
	}
	return nil, haystack, coder.ErrNotFound
}

// findEnv returns a single environment by name (if it exists.).
func findEnv(ctx context.Context, client *coder.Client, envName, userEmail string) (*coder.Environment, error) {
	env, haystack, err := searchForEnv(ctx, client, envName, userEmail)
	if err != nil {
		return nil, clog.Fatal(
			"failed to find environment",
			fmt.Sprintf("environment %q not found in %q", envName, haystack),
			clog.BlankLine,
			clog.Tipf("run \"coder envs ls\" to view your environments"),
		)
	}
	return env, nil
}

// waitForEnvOnline will wait up to the maximum time allowed for an environment to have a 'ON' status.
// It expects the status to be 'Creating'. If the status is 'failed' or 'off', this function will also terminate
// The returned values is the last known env status and an error if the env never came online
func waitForEnvOnline(ctx context.Context, client *coder.Client, envID string, maxWait time.Duration) (status coder.EnvironmentStatus, err error) {
	status = coder.EnvironmentUnknown
	tmoCtx, cancel := context.WithTimeout(ctx, maxWait)
	// Retry every 50ms until we get it or we timeout.
	retry := time.NewTicker(time.Millisecond * 50)
	defer cancel()

	for {
		select {
		case <-tmoCtx.Done():
			return status, context.DeadlineExceeded
		case <-retry.C:
			env, err := client.EnvironmentByID(ctx, envID)
			if err != nil {
				// If this api call failed, it will likely fail again, no point to retry and make the user wait
				return status, err
			}

			status = env.LatestStat.ContainerStatus
			switch status {
			case coder.EnvironmentOn:
				return status, nil // Exit function, env is online
			case coder.EnvironmentOff:
				// Well waiting won't help...
				return status, fmt.Errorf("environment is off and will not come online")
			case coder.EnvironmentFailed:
				// Well waiting won't help...
				return status, fmt.Errorf("environment has failed to build and will not come online")
			}
		}
	}
}

type findImgConf struct {
	email   string
	imgName string
	orgName string
}

func findImg(ctx context.Context, client *coder.Client, conf findImgConf) (*coder.Image, error) {
	switch {
	case conf.email == "":
		return nil, xerrors.New("user email unset")
	case conf.imgName == "":
		return nil, xerrors.New("image name unset")
	}

	imgs, err := getImgs(ctx, client, getImgsConf{
		email:   conf.email,
		orgName: conf.orgName,
	})
	if err != nil {
		return nil, err
	}

	var possibleMatches []coder.Image

	// The user may provide an image thats not an exact match
	// to one of their imported images but they may be close.
	// We can assist the user by collecting images that contain
	// the user provided image flag value as a substring.
	for _, img := range imgs {
		// If it's an exact match we can just return and exit.
		if img.Repository == conf.imgName {
			return &img, nil
		}
		if strings.Contains(img.Repository, conf.imgName) {
			possibleMatches = append(possibleMatches, img)
		}
	}

	if len(possibleMatches) == 0 {
		return nil, xerrors.New("image not found - did you forget to import this image?")
	}

	lines := []string{clog.Hintf("Did you mean?")}

	for _, img := range possibleMatches {
		lines = append(lines, fmt.Sprintf("  %s", img.Repository))
	}
	return nil, clog.Fatal(
		fmt.Sprintf("image %s not found", conf.imgName),
		lines...,
	)
}

type getImgsConf struct {
	email   string
	orgName string
}

func getImgs(ctx context.Context, client *coder.Client, conf getImgsConf) ([]coder.Image, error) {
	u, err := client.UserByEmail(ctx, conf.email)
	if err != nil {
		return nil, err
	}

	orgs, err := client.Organizations(ctx)
	if err != nil {
		return nil, err
	}

	orgs = lookupUserOrgs(u, orgs)

	for _, org := range orgs {
		imgs, err := client.OrganizationImages(ctx, org.ID)
		if err != nil {
			return nil, err
		}
		// If orgName is set we know the user is a multi-org member
		// so we should only return the imported images that beong to the org they specified.
		if conf.orgName != "" && conf.orgName == org.Name {
			return imgs, nil
		}

		if conf.orgName == "" {
			// if orgName is unset we know the user is only part of one org.
			return imgs, nil
		}
	}
	return nil, xerrors.Errorf("org name %q not found", conf.orgName)
}

func isMultiOrgMember(ctx context.Context, client *coder.Client, email string) (bool, error) {
	u, err := client.UserByEmail(ctx, email)
	if err != nil {
		return false, xerrors.New("email not found")
	}

	orgs, err := client.Organizations(ctx)
	if err != nil {
		return false, err
	}
	return len(lookupUserOrgs(u, orgs)) > 1, nil
}
