//go:generate ./generic.gen InterceptMap *github.com/telepresenceio/telepresence/rpc/v2/manager.InterceptInfo Id
//go:generate ./generic.gen AgentMap     *github.com/telepresenceio/telepresence/rpc/v2/manager.AgentInfo     Name
//go:generate ./generic.gen ClientMap    *github.com/telepresenceio/telepresence/rpc/v2/manager.ClientInfo    Name

package watchable
