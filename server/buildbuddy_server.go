package buildbuddy_server

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/buildbuddy-io/buildbuddy/server/build_event_protocol/build_event_handler"
	"github.com/buildbuddy-io/buildbuddy/server/environment"
	"github.com/buildbuddy-io/buildbuddy/server/tables"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"google.golang.org/grpc"

	bzpb "proto/bazel_config"
	grpb "proto/group"
	inpb "proto/invocation"
	uspb "proto/user"

	bspb "google.golang.org/genproto/googleapis/bytestream"
)

const (
	bytestreamProtocolPrefix = "bytestream://"
)

type BuildBuddyServer struct {
	env environment.Env
}

func NewBuildBuddyServer(env environment.Env) (*BuildBuddyServer, error) {
	return &BuildBuddyServer{
		env: env,
	}, nil
}

func (s *BuildBuddyServer) GetInvocation(ctx context.Context, req *inpb.GetInvocationRequest) (*inpb.GetInvocationResponse, error) {
	if req.GetLookup().GetInvocationId() == "" {
		return nil, status.InvalidArgumentErrorf("GetInvocationRequest must contain a valid invocation_id")
	}

	inv, err := build_event_handler.LookupInvocation(s.env, ctx, req.GetLookup().GetInvocationId())
	if err != nil {
		return nil, err
	}
	return &inpb.GetInvocationResponse{
		Invocation: []*inpb.Invocation{
			inv,
		},
	}, nil
}

func (s *BuildBuddyServer) SearchInvocation(ctx context.Context, req *inpb.SearchInvocationRequest) (*inpb.SearchInvocationResponse, error) {
	if req == nil {
		return nil, status.InvalidArgumentErrorf("SearchInvocationRequest cannot be empty")
	}
	searcher := s.env.GetInvocationSearchService()
	if searcher == nil {
		return nil, fmt.Errorf("No searcher was configured")
	}
	if req.Query == nil {
		return nil, fmt.Errorf("A query must be provided")
	}
	return searcher.QueryInvocations(ctx, req)
}

func makeGroups(grps []*tables.Group) []*grpb.Group {
	r := make([]*grpb.Group, 0)
	for _, g := range grps {
		r = append(r, &grpb.Group{
			Id:          g.GroupID,
			Name:        g.Name,
			OwnedDomain: g.OwnedDomain,
		})
	}
	return r
}

func (s *BuildBuddyServer) GetUser(ctx context.Context, req *uspb.GetUserRequest) (*uspb.GetUserResponse, error) {
	userDB := s.env.GetUserDB()
	if userDB == nil {
		return nil, status.UnimplementedError("Not Implemented")
	}
	tu, err := userDB.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	if tu == nil {
		return nil, status.UnauthenticatedError("User not found")
	}
	return &uspb.GetUserResponse{
		DisplayUser: tu.ToProto(),
		UserGroup:   makeGroups(tu.Groups),
	}, nil
}

func (s *BuildBuddyServer) CreateUser(ctx context.Context, req *uspb.CreateUserRequest) (*uspb.CreateUserResponse, error) {
	auth := s.env.GetAuthenticator()
	userDB := s.env.GetUserDB()
	if auth == nil || userDB == nil {
		return nil, status.UnimplementedError("Not Implemented")
	}
	// If null authenticator is installed creating will fail so exit early.
	if ut, err := auth.GetUserToken(ctx); ut == nil || err != nil {
		return nil, status.UnimplementedError("Not Implemented")
	}
	tu := &tables.User{}
	if err := auth.FillUser(ctx, tu); err != nil {
		return nil, err
	}
	if err := userDB.InsertUser(ctx, tu); err != nil {
		return nil, err
	}
	return &uspb.CreateUserResponse{
		DisplayUser: tu.ToProto(),
	}, nil
}

func makeConfigOption(lifecycle, flagName, flagValue string) *bzpb.ConfigOption {
	return &bzpb.ConfigOption{
		Body:            fmt.Sprintf("%s --%s=%s", lifecycle, flagName, flagValue),
		OptionLifecycle: lifecycle,
		FlagName:        flagName,
		FlagValue:       flagValue,
	}
}

func assembleURL(host, scheme, port string) string {
	// Strip any existing port from host if we're setting a port.
	components := strings.Split(host, ":")
	if len(components) > 1 && port != "" {
		host = components[0]
	}

	url := scheme + "//" + host

	// Only append port if it's set and not 80.
	if port != "" && port != "80" {
		url = url + ":" + port
	}
	return url
}

func (s *BuildBuddyServer) getGroupLoginPW(ctx context.Context) (string, string) {
	if userDB := s.env.GetUserDB(); userDB != nil {
		if tu, _ := userDB.GetUser(ctx); tu != nil {
			for _, g := range tu.Groups {
				if g.OwnedDomain != "" && g.WriteToken != "" {
					return g.GroupID, g.WriteToken
				}
			}
			// Still here? This user might have a self-owned group, let's check for that.
			for _, g := range tu.Groups {
				if g.GroupID == strings.Replace(tu.UserID, "US", "GR", 1) && g.WriteToken != "" {
					return g.GroupID, g.WriteToken
				}
			}
			// Finally, fall back to any group with a WriteToken. This will be the
			// default group for on-prem use cases.
			for _, g := range tu.Groups {
				if g.WriteToken != "" {
					return g.GroupID, g.WriteToken
				}
			}
		}
	}
	return "", ""
}

func insertPassword(rawURL, username, password string) string {
	if username == "" && password == "" {
		return rawURL
	}
	writeKey := username + ":" + password
	return strings.Replace(rawURL, "://", "://"+writeKey+"@", 1)
}

func getIntFlag(flagName string, defaultVal string) string {
	f := flag.Lookup(flagName)
	if f == nil {
		return defaultVal
	}
	return f.Value.String()
}

func (s *BuildBuddyServer) GetBazelConfig(ctx context.Context, req *bzpb.GetBazelConfigRequest) (*bzpb.GetBazelConfigResponse, error) {
	configOptions := make([]*bzpb.ConfigOption, 0)

	// Use config urls if they're set and fall back to host & protocol from request if not.
	resultsURL := s.env.GetConfigurator().GetAppBuildBuddyURL()
	if resultsURL == "" {
		resultsURL = assembleURL(req.Host, req.Protocol, "")
	}
	configOptions = append(configOptions, makeConfigOption("build", "bes_results_url", resultsURL+"/invocation/"))

	eventsAPIURL := s.env.GetConfigurator().GetAppEventsAPIURL()
	if eventsAPIURL == "" {
		grpcPort := getIntFlag("grpc_port", "1985")
		eventsAPIURL = assembleURL(req.Host, "grpc:", grpcPort)
	}
	username, pw := s.getGroupLoginPW(ctx)
	eventsAPIURL = insertPassword(eventsAPIURL, username, pw)
	configOptions = append(configOptions, makeConfigOption("build", "bes_backend", eventsAPIURL))

	if s.env.GetCache() != nil {
		cacheAPIURL := s.env.GetConfigurator().GetAppCacheAPIURL()
		if cacheAPIURL == "" {
			grpcPort := getIntFlag("grpc_port", "1985")
			cacheAPIURL = assembleURL(req.Host, "grpc:", grpcPort)
		}
		username, pw := s.getGroupLoginPW(ctx)
		cacheAPIURL = insertPassword(cacheAPIURL, username, pw)
		configOptions = append(configOptions, makeConfigOption("build", "remote_cache", cacheAPIURL))
	}

	return &bzpb.GetBazelConfigResponse{
		ConfigOption: configOptions,
	}, nil
}

func (s *BuildBuddyServer) GetInvocationStat(ctx context.Context, req *inpb.GetInvocationStatRequest) (*inpb.GetInvocationStatResponse, error) {
	if iss := s.env.GetInvocationStatService(); iss != nil {
		return iss.GetInvocationStat(ctx, req)
	}
	return nil, status.UnimplementedError("Not implemented")
}

type bsLookup struct {
	URL      *url.URL
	Filename string
}

func getBestFilename(filename, blobname string) string {
	// First try to use the filename parameter
	parts := strings.Split(filename, "/")
	name := parts[len(parts)-1]
	if name != "" {
		return name
	}
	// Next try to extract a reasonable name from the blob name.
	parts = strings.Split(blobname, "/")
	if len(parts) == 4 {
		return parts[2]
	}
	// Finally, just return the blobname.
	return blobname
}

func parseByteStreamURL(bsURL, filename string) (*bsLookup, error) {
	if strings.HasPrefix(bsURL, bytestreamProtocolPrefix) {
		u, err := url.Parse(bsURL)
		if err != nil {
			return nil, err
		}
		return &bsLookup{
			URL:      u,
			Filename: getBestFilename(filename, u.RequestURI()),
		}, nil
	}
	return nil, fmt.Errorf("unparsable bytestream URL: '%s'", bsURL)
}

// Handle requests for build logs and artifacts by looking them up in from our
// cache server using the bytestream API.
func (s *BuildBuddyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	lookup, err := parseByteStreamURL(params.Get("bytestream_url"), params.Get("filename"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Optionally, configure HTTP basic-auth.
	dialOptions := make([]grpc.DialOption, 0)
	if lookup.URL.User != nil {
		authStr := lookup.URL.User.String() + "@" + lookup.URL.Host
		dialOptions = append(dialOptions, grpc.WithAuthority(authStr))
	}
	dialOptions = append(dialOptions, grpc.WithInsecure())

	// TODO(siggisim): Support GRPCS caches.
	grpcPort := getIntFlag("grpc_port", "1985")
	grpcsPort := getIntFlag("grpcs_port", "1986")
	if lookup.URL.Port() == grpcsPort {
		lookup.URL.Host = lookup.URL.Hostname() + ":" + grpcPort
	}

	// Connect to host/port and create a new client
	conn, err := grpc.Dial(lookup.URL.Host, dialOptions...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer conn.Close()
	client := bspb.NewByteStreamClient(conn)

	// Request the file bytestream
	req := &bspb.ReadRequest{
		ResourceName: strings.TrimPrefix(lookup.URL.RequestURI(), "/"), // trim leading "/"
		ReadOffset:   0,                                                // started from the bottom now we here
		ReadLimit:    0,                                                // no limit
	}
	stream, err := client.Read(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Stream the file back to our client
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", lookup.Filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	for {
		rsp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(rsp.Data)
	}
}
