//go:generate ./generic.gen InterceptMap *github.com/datawire/telepresence2/rpc/manager.InterceptInfo Id
//go:generate ./generic.gen AgentMap     *github.com/datawire/telepresence2/rpc/manager.AgentInfo     Name
//go:generate ./generic.gen ClientMap    *github.com/datawire/telepresence2/rpc/manager.ClientInfo    Name

package watchable
