package bsc

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"

	"golang.org/x/net/html"
)

var redirectionRejectedError = errors.New("redirect occurred")
var scheduleListViewPath string = "/EMPLOYEE/HRMS/c/SA_LEARNER_SERVICES.SSR_SSENRL_LIST.GBL?Page=SSR_SSENRL_LIST"

// A Client makes requests to a University's Student Center.
type Client struct {
	// authLock ensures that no concurrent requests are made during the re-authentication process.
	// It also ensures that the client does not authenticate more than once concurrently.
	authLock sync.RWMutex

	client   http.Client
	username string
	password string
	uni      UniversityEngine
}

// NewClient creates a new Client which authenticates with the supplied username, password, and
// UniversityEngine.
func NewClient(username, password string, uni UniversityEngine) *Client {
	jar, _ := cookiejar.New(nil)

	tlsConfig := tls.Config{
		CipherSuites: []uint16{
			tls.TLS_RSA_WITH_RC4_128_SHA,
			tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_RC4_128_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_RC4_128_SHA,
			tls.TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		},
	}
	transport := &http.Transport{
		TLSClientConfig: &tlsConfig,
	}

	httpClient := http.Client{
		Jar: jar,
		CheckRedirect: rejectRedirect,
		Transport: transport,
	}
	return &Client{sync.RWMutex{}, httpClient, username, password, uni}
}

// Authenticate authenticates with the university's server.
//
// You should call this after creating a Client. However, if you do not, it will automatically be
// called after the first request fails.
func (c *Client) Authenticate() error {
	c.authLock.Lock()
	defer c.authLock.Unlock()
	return c.uni.Authenticate(c)
}

// FetchCurrentSchedule downloads the user's current schedule.
//
// If fetchMoreInfo is true, the components of each course will have extra information.
func (c *Client) FetchSchedule(fetchMoreInfo bool) ([]Course, error) {
	// TODO: GET page, then check if a <form> exists, then extract the name of the radio buttons?
	postData := url.Values{}
	postData.Add("SSR_DUMMY_RECV1$sels$0", "0")

	if resp, err := c.RequestPagePost(scheduleListViewPath, postData); err != nil {
		return nil, err
	} else {
		defer resp.Body.Close()

		contents, err := ioutil.ReadAll(resp.Body)
		fmt.Println(string(contents))

		nodes, err := html.ParseFragment(resp.Body, nil)
		if err != nil {
			return nil, err
		}
		if len(nodes) != 1 {
			return nil, errors.New("invalid number of root elements")
		}

		courses, err := parseSchedule(nodes[0])
		if err != nil {
			return nil, err
		}
		if fetchMoreInfo {
			c.authLock.RLock()
			defer c.authLock.RUnlock()
			if err := fetchExtraScheduleInfo(&c.client, courses, nodes[0]); err != nil {
				return nil, err
			}
		}
		return courses, nil
	}
}

// RequestPage requests a page relative to the PeopleSoft root. This will automatically
// re-authenticate if the session has timed out.
// If the request fails for any reason (including a redirect), the returned response is nil.
func (c *Client) RequestPage(page string) (*http.Response, error) {
	requestURL := c.uni.RootURL() + page
	c.authLock.RLock()
	resp, err := c.client.Get(requestURL)
	c.authLock.RUnlock()
	if err != nil && !isRedirectError(err) {
		return nil, err
	} else if err == nil {
		return resp, nil
	}

	resp.Body.Close()

	if err := c.Authenticate(); err != nil {
		return nil, err
	}

	c.authLock.RLock()
	resp, err = c.client.Get(requestURL)
	c.authLock.RUnlock()
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, err
	} else {
		return resp, nil
	}
}

func (c *Client) RequestPagePost(page string, postData url.Values) (*http.Response, error) {
	requestURL := c.uni.RootURL() + page
	c.authLock.RLock()
	resp, err := c.client.PostForm(requestURL, postData)
	c.authLock.RUnlock()
	if err != nil && !isRedirectError(err) {
		return nil, err
	} else if err == nil {
		return resp, nil
	}

	resp.Body.Close()

	if err := c.Authenticate(); err != nil {
		return nil, err
	}

	c.authLock.RLock()
	resp, err = c.client.Get(requestURL)
	c.authLock.RUnlock()
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, err
	} else {
		return resp, nil
	}
}

// postGenericLoginForm uses parseGenericLoginForm on the given page and POSTs the username and
// password. It may fail at several points. If all is successful, it returns the result of the POST.
//
// Since this should only be called during authentication, it assumes that c.authLock is already
// locked in write mode.
//
// If the post results in a redirect, this may return a non-nil response with a non-nil error.
func (c *Client) postGenericLoginForm(authPageURL string) (*http.Response, error) {
	res, err := c.client.Get(authPageURL)
	if res != nil {
		defer res.Body.Close()
	}
	if err != nil {
		return nil, err
	}

	formInfo, err := parseGenericLoginForm(res)
	if err != nil {
		return nil, err
	}

	fields := formInfo.otherFields
	fields.Add(formInfo.usernameField, c.username)
	fields.Add(formInfo.passwordField, c.password)

	return c.client.PostForm(formInfo.action, fields)
}

// isRedirectError returns true if an error is a redirectionRejectedError wrapped in url.Error.
func isRedirectError(err error) bool {
	if urlError, ok := err.(*url.Error); !ok {
		return false
	} else {
		return urlError.Err == redirectionRejectedError
	}
}

// rejectRedirect always returns an error.
func rejectRedirect(_ *http.Request, _ []*http.Request) error {
	return redirectionRejectedError
}
