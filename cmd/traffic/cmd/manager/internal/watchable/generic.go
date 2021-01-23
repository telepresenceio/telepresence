//go:generate ./generic.gen InterceptMap *github.com/datawire/telepresence2/rpc/v2/manager.InterceptInfo Id
//go:generate ./generic.gen AgentMap     *github.com/datawire/telepresence2/rpc/v2/manager.AgentInfo     Name
//go:generate ./generic.gen ClientMap    *github.com/datawire/telepresence2/rpc/v2/manager.ClientInfo    Name

package watchable
