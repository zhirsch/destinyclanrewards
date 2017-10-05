package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/zhirsch/destiny2-api/client/destiny2"
	"github.com/zhirsch/destiny2-api/client/operations"
	db "github.com/zhirsch/destiny2-db"

	"github.com/go-openapi/runtime"
	runtime_client "github.com/go-openapi/runtime/client"
	"github.com/zhirsch/destiny2-api/client"
	"github.com/zhirsch/destiny2-api/client/group_v2"
	"github.com/zhirsch/destiny2-api/models"
)

var (
	flagAPIKey   = flag.String("apikey", "", "the Bungie API key")
	flagUsername = flag.String("user", "", "the user to query")
	flagVerbose  = flag.Bool("verbose", false, "enable verbose output")

	logger *log.Logger
)

func getDestinyUser(api *client.BungieNet, auth runtime.ClientAuthInfoWriter, username string) (*models.UserUserInfoCard, error) {
	logger.Printf("getting destiny user %q", username)
	params := destiny2.NewDestiny2SearchDestinyPlayerParams()
	params.SetDisplayName(username)
	params.SetMembershipType(-1)
	resp, err := api.Destiny2.Destiny2SearchDestinyPlayer(params, auth)
	if err != nil {
		logger.Fatal(err)
	}
	if len(resp.Payload.Response) != 1 {
		return nil, errors.Errorf("found multiple destiny users named %q", username)
	}
	return resp.Payload.Response[0], nil
}

func getClan(api *client.BungieNet, auth runtime.ClientAuthInfoWriter, user *models.UserUserInfoCard) (*models.GroupsV2GroupV2, error) {
	logger.Printf("getting clan for destiny user %q", user.DisplayName)
	params := group_v2.NewGroupV2GetGroupsForMemberParams()
	params.SetFilter(0)
	params.SetGroupType(1)
	params.SetMembershipID(user.MembershipID)
	params.SetMembershipType(int32(user.MembershipType))
	resp, err := api.GroupV2.GroupV2GetGroupsForMember(params, auth)
	if err != nil {
		return nil, err
	}
	if len(resp.Payload.Response.Results) != 1 {
		return nil, errors.Errorf("found multiple clans for destiny user %q", user.DisplayName)
	}
	return resp.Payload.Response.Results[0].Group, nil
}

func getClanByDestinyUser(api *client.BungieNet, auth runtime.ClientAuthInfoWriter, username string) (*models.GroupsV2GroupV2, error) {
	user, err := getDestinyUser(api, auth, username)
	if err != nil {
		return nil, err
	}
	return getClan(api, auth, user)
}

func getCharacters(api *client.BungieNet, auth runtime.ClientAuthInfoWriter, user *models.UserUserInfoCard) ([]models.DestinyEntitiesCharactersDestinyCharacterComponent, error) {
	logger.Printf("getting characters for destiny user %v (%q)", user.MembershipID, user.DisplayName)
	params := destiny2.NewDestiny2GetProfileParams()
	params.SetDestinyMembershipID(user.MembershipID)
	params.SetMembershipType(int32(user.MembershipType))
	params.SetComponents([]int64{200})
	resp, err := api.Destiny2.Destiny2GetProfile(params, auth)
	if err != nil {
		return nil, err
	}
	var characters []models.DestinyEntitiesCharactersDestinyCharacterComponent
	if resp.Payload.Response == nil {
		logger.Printf("no characters for user %v (%q)", user.MembershipID, user.DisplayName)
		return characters, nil
	}
	for _, v := range resp.Payload.Response.Characters.Data {
		characters = append(characters, v)
	}
	return characters, nil
}

func getMembers(api *client.BungieNet, auth runtime.ClientAuthInfoWriter, groupID int64) ([]*models.UserUserInfoCard, error) {
	var currentPage int32 = 1
	var members []*models.UserUserInfoCard
	for {
		logger.Printf("getting clan members (page %v)", currentPage)
		params := group_v2.NewGroupV2GetMembersOfGroupParams()
		params.SetCurrentpage(currentPage)
		params.SetGroupID(groupID)
		resp, err := api.GroupV2.GroupV2GetMembersOfGroup(params, auth)
		if err != nil {
			return nil, err
		}
		for _, result := range resp.Payload.Response.Results {
			members = append(members, result.DestinyUserInfo)
		}
		if !resp.Payload.Response.HasMore {
			break
		}
		currentPage++
	}
	logger.Printf("found %v members", len(members))
	return members, nil
}

func getRewards(api *client.BungieNet, auth runtime.ClientAuthInfoWriter, groupID int64) (*models.DestinyMilestonesDestinyMilestone, error) {
	logger.Printf("getting clan reward status for clan %v", groupID)
	params := destiny2.NewDestiny2GetClanWeeklyRewardStateParams()
	params.SetGroupID(groupID)
	resp, err := api.Destiny2.Destiny2GetClanWeeklyRewardState(params, auth)
	if err != nil {
		return nil, err
	}
	return resp.Payload.Response, nil
}

func getActivities(api *client.BungieNet, auth runtime.ClientAuthInfoWriter, start, end time.Time, user *models.UserUserInfoCard, character models.DestinyEntitiesCharactersDestinyCharacterComponent, mode int32) ([]*models.DestinyHistoricalStatsDestinyHistoricalStatsPeriodGroup, error) {
	params := operations.NewDestiny2GetActivityHistoryParams()
	params.SetCharacterID(character.CharacterID)
	params.SetDestinyMembershipID(user.MembershipID)
	params.SetMembershipType(int32(user.MembershipType))
	var count int32 = 100
	params.SetCount(&count)
	params.SetMode(&mode)
	var page int32
	var activities []*models.DestinyHistoricalStatsDestinyHistoricalStatsPeriodGroup
	for {
		logger.Printf("getting %v activities for character %v of destiny user %v (%q) page %v", mode, character.CharacterID, user.MembershipID, user.DisplayName, page)
		params.SetPage(&page)
		resp, err := api.Operations.Destiny2GetActivityHistory(params, auth)
		if err != nil {
			return nil, err
		}
		found := false
		for _, activity := range resp.Payload.Response.Activities {
			startTime := time.Time(activity.Period)
			if startTime.Before(start) {
				continue
			}
			endTime := startTime.Add(time.Duration(activity.Values["activityDurationSeconds"].Basic.Value) * time.Second)
			if endTime.After(end) {
				continue
			}
			activities = append(activities, activity)
			found = true
		}
		if !found {
			break
		}
		if len(resp.Payload.Response.Activities) < int(count) {
			break
		}
		page++
	}
	return activities, nil
}
func getFireteam(api *client.BungieNet, auth runtime.ClientAuthInfoWriter, instanceID int64) ([]*models.UserUserInfoCard, error) {
	logger.Printf("getting fireteam for instance %v", instanceID)
	params := destiny2.NewDestiny2GetPostGameCarnageReportParams()
	params.SetActivityID(instanceID)
	resp, err := api.Destiny2.Destiny2GetPostGameCarnageReport(params, auth)
	if err != nil {
		return nil, err
	}
	var fireteam []*models.UserUserInfoCard
	for _, entry := range resp.Payload.Response.Entries {
		if entry.Values["completed"].Basic.Value == 0 {
			continue
		}
		fireteam = append(fireteam, entry.Player.DestinyUserInfo)
	}
	return fireteam, nil
}

type byMembershipID []*models.UserUserInfoCard

func (b byMembershipID) Len() int           { return len(b) }
func (b byMembershipID) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }
func (b byMembershipID) Less(i, j int) bool { return b[i].MembershipID < b[j].MembershipID }

type completion struct {
	end             time.Time
	fireteamMembers []*models.UserUserInfoCard
}

func (c *completion) getFireteamAsString() string {
	var arr []string
	for _, fireteamMember := range c.fireteamMembers {
		arr = append(arr, fireteamMember.DisplayName)
	}
	sort.Strings(arr)
	return strings.Join(arr, ",")
}

func getEarliestClanCompletion(api *client.BungieNet, auth runtime.ClientAuthInfoWriter, start, end time.Time, clanMemberIDs map[int64]bool, clanMember *models.UserUserInfoCard, characters []models.DestinyEntitiesCharactersDestinyCharacterComponent, mode int32, earliest *completion) (*completion, error) {
	for _, character := range characters {
		activities, err := getActivities(api, auth, start, end, clanMember, character, mode)
		if err != nil {
			return nil, err
		}
		for _, activity := range activities {
			c := &completion{
				end: time.Time(activity.Period).Add(time.Duration(activity.Values["activityDurationSeconds"].Basic.Value) * time.Second),
			}
			if activity.Values["completed"].Basic.Value == 0 {
				continue
			}
			var victory bool
			if standing, ok := activity.Values["standing"]; ok {
				victory = (standing.Basic.Value == 0)
			} else if completionReason, ok := activity.Values["completionReason"]; ok {
				victory = (completionReason.Basic.Value == 0)
			} else {
				logger.Panicf("unknown victory state for activity %v", activity.ActivityDetails.InstanceID)
			}
			if !victory {
				continue
			}
			if earliest != nil && (c.end.After(earliest.end) || c.end == earliest.end) {
				continue
			}
			fireteamMembers, err := getFireteam(api, auth, activity.ActivityDetails.InstanceID)
			if err != nil {
				return nil, err
			}
			for _, fireteamMember := range fireteamMembers {
				if _, ok := clanMemberIDs[fireteamMember.MembershipID]; ok {
					logger.Printf("clan member %v (%q) was a member of the fireteam", fireteamMember.MembershipID, fireteamMember.DisplayName)
					c.fireteamMembers = append(c.fireteamMembers, fireteamMember)
				}
			}
			var minClanMembersNeeded int
			switch mode {
			case 4: // Raid
				minClanMembersNeeded = 3
			case 16: // NF
				fallthrough
			case 39: // Trials
				fallthrough
			case 5: // Crucible
				minClanMembersNeeded = 2
			default:
				logger.Panicf("unknown mode: %v", mode)
			}
			if len(c.fireteamMembers) < minClanMembersNeeded {
				logger.Printf("at least half the members were not part of the clan")
				continue
			}
			earliest = c
		}
	}
	return earliest, nil
}

func getEarliestClanCompletions(api *client.BungieNet, auth runtime.ClientAuthInfoWriter, start, end time.Time, clanMembers []*models.UserUserInfoCard) (*completion, *completion, *completion, *completion, error) {
	var (
		raid      *completion
		nightfall *completion
		trials    *completion
		crucible  *completion
	)
	// Build a set of the clan member IDs.
	clanMemberIDs := make(map[int64]bool)
	for _, clanMember := range clanMembers {
		clanMemberIDs[clanMember.MembershipID] = true
	}
	for _, clanMember := range clanMembers {
		characters, err := getCharacters(api, auth, clanMember)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		raid, err = getEarliestClanCompletion(api, auth, start, end, clanMemberIDs, clanMember, characters, 4, raid)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		nightfall, err = getEarliestClanCompletion(api, auth, start, end, clanMemberIDs, clanMember, characters, 16, nightfall)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		trials, err = getEarliestClanCompletion(api, auth, start, end, clanMemberIDs, clanMember, characters, 39, trials)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		crucible, err = getEarliestClanCompletion(api, auth, start, end, clanMemberIDs, clanMember, characters, 5, crucible)
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}
	return raid, nightfall, trials, crucible, nil
}

func main() {
	flag.Parse()

	if *flagVerbose {
		logger = log.New(os.Stderr, "", log.LstdFlags)
	} else {
		logger = log.New(ioutil.Discard, "", log.LstdFlags)
	}

	// Create the API client and authentication.
	api := client.Default
	auth := runtime_client.APIKeyAuth("X-API-Key", "header", *flagAPIKey)

	// Open the manifest database.
	db, err := db.Open(api, auth)
	if err != nil {
		logger.Fatal(err)
	}

	// Get the clan.
	clan, err := getClanByDestinyUser(api, auth, *flagUsername)
	if err != nil {
		logger.Fatal(err)
	}

	// Get the clan rewards.
	rewards, err := getRewards(api, auth, clan.GroupID)
	if err != nil {
		logger.Fatal(err)
	}
	start, end := time.Time(rewards.StartDate), time.Time(rewards.EndDate)

	// Get the clan members.
	clanMembers, err := getMembers(api, auth, clan.GroupID)
	if err != nil {
		logger.Fatal(err)
	}
	sort.Sort(byMembershipID(clanMembers))

	// Print out the reward state.
	milestoneDefinitionInterface, err := db.Get("DestinyMilestoneDefinition", 4253138191, &models.DestinyDefinitionsMilestonesDestinyMilestoneDefinition{})
	if err != nil {
		logger.Fatal(err)
	}
	milestoneDefinition := milestoneDefinitionInterface.(*models.DestinyDefinitionsMilestonesDestinyMilestoneDefinition)
	for _, reward := range rewards.Rewards {
		raid, nightfall, trials, crucible, err := getEarliestClanCompletions(api, auth, start, end, clanMembers)
		if err != nil {
			logger.Fatal(err)
		}

		rewardCategoryHashStr := strconv.FormatUint(uint64(reward.RewardCategoryHash), 10)
		rewardCategory := milestoneDefinition.Rewards[rewardCategoryHashStr]
		fmt.Println(rewardCategory.DisplayProperties.Name)
		for _, entry := range reward.Entries {
			earned := " "
			if entry.Earned {
				earned = "✓"
			}
			rewardEntryHashStr := strconv.FormatUint(uint64(entry.RewardEntryHash), 10)
			name := rewardCategory.RewardEntries[rewardEntryHashStr].DisplayProperties.Name
			fmt.Printf(" %s %v\n", earned, name)
		}
		if raid != nil {
			fmt.Printf("Raid      completed at %v by %v\n", raid.end, raid.getFireteamAsString())
		}
		if nightfall != nil {
			fmt.Printf("Nightfall completed at %v by %v\n", nightfall.end, nightfall.getFireteamAsString())
		}
		if trials != nil {
			fmt.Printf("Trials    completed at %v by %v\n", trials.end, trials.getFireteamAsString())
		}
		if crucible != nil {
			fmt.Printf("Crucible  completed at %v by %v\n", crucible.end, crucible.getFireteamAsString())
		}
		fmt.Println()

		start = start.AddDate(0, 0, -7)
		end = end.AddDate(0, 0, -7)
	}
}
