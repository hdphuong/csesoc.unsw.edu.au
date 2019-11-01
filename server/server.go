package main

import (
	"context"
	"crypto/sha256"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gopkg.in/ldap.v2"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// H - interface for sending JSON
type H map[string]interface{}

// Post - struct to contain post data
type Post struct {
	postID           int
	postTitle        string
	postSubtitle     string
	postType         string
	postCategory     int
	createdOn        time.Time
	lastEditedOn     time.Time
	postContent      string
	postLinkGithub   string
	postLinkFacebook string
	showInMenu       bool
}

// Category - struct to contain category data
type Category struct {
	categoryID   int
	categoryName string
	index        int
}

// User - struct to contain user data
type User struct {
	userID    string //sha256 the zid
	userToken string
	role      string
}

// Sponsor - struct to contain sponsor data
type Sponsor struct {
	sponsorID   uuid.UUID
	sponsorName string
	sponsorLogo string
	sponsorTier string
	expiry      int64
}

// Claims - struct to store jwt data
type Claims struct {
	hashedZID [32]byte
	firstName string
	jwt.StandardClaims
}

func main() {
	// Create new instance of echo
	e := echo.New()

	servePages(e)
	serveAPI(e)

	// Start echo instance on 1323 port
	e.Logger.Fatal(e.Start(":1323"))
}

func servePages(e *echo.Echo) {
	// Setup our assetHandler and point it to our static build location
	assetHandler := http.FileServer(http.Dir("../dist/"))

	// Setup a new echo route to load the build as our base path
	e.GET("/", echo.WrapHandler(assetHandler))

	// Serve our static assists under the /static/ endpoint
	e.GET("/js/*", echo.WrapHandler(assetHandler))
	e.GET("/css/*", echo.WrapHandler(assetHandler))

	echo.NotFoundHandler = func(c echo.Context) error {
		// render your 404 page
		return c.String(http.StatusNotFound, "not found page")
	}
}

func serveAPI(e *echo.Echo) {
	//Set client options
	clientOptions := options.Client().ApplyURI("mongodb://localhost:27017")
	//Connect to MongoDB
	client, err := mongo.Connect(context.TODO(), clientOptions)
	if err != nil {
		log.Fatal(err)
	}
	//Check connection
	err = client.Ping(context.TODO(), nil)
	if err != nil {
		log.Fatal(err)
	}

	// Creating collections
	postsCollection := client.Database("csesoc").Collection("posts")
	catCollection := client.Database("csesoc").Collection("categories")
	sponsorCollection := client.Database("csesoc").Collection("sponsors")
	userCollection := client.Database("csesoc").Collection("users")

	// Add more API routes here
	e.GET("/api/v1/test", func(c echo.Context) error {
		return c.String(http.StatusOK, "Hello, World!")
	})

	e.POST("/login/", login(userCollection))

	// Routes for posts
	e.GET("/post/:id/", getPost(postsCollection))
	e.GET("/posts/", getAllPosts(postsCollection))
	e.POST("/post/", newPost(postsCollection))
	e.PUT("/post/:id/", updatePost(postsCollection))
	e.DELETE("/post/:id/", deletePost(postsCollection))

	// Routes for categories
	e.GET("/category/:id/", getCat(catCollection))
	e.POST("/category/", newCat(catCollection))
	e.PATCH("/category/", patchCat(catCollection))
	e.DELETE("/category/", deleteCat(catCollection))

	// Routes for sponsors
	e.POST("/sponsor/", newSponsor(sponsorCollection))
	e.DELETE("/sponsor/", deleteSponsor(sponsorCollection))
}

func login(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Connect to UNSW LDAP server
		l, err := ldap.Dial("tcp", "ad.unsw.edu.au")
		if err != nil {
			log.Fatal(err)
		}

		// Attempt to sign in using credentials
		zid := c.FormValue("zid")
		hashedZID := sha256.Sum256([]byte(zid))
		username := zid + "ad.unsw.edu.au"
		password := c.FormValue("password")

		err = l.Bind(username, password)
		if err != nil {
			log.Fatal(err)
		}

		// Retrieve first name from Identity Manager
		baseDN := "OU=IDM_People,OU=IDM,DC=ad,DC=unsw,DC=edu,DC=au"
		searchScope := ldap.ScopeWholeSubtree
		aliases := ldap.NeverDerefAliases
		retrieveAttributes := []string{"givenName"}
		searchFilter := "cn=" + username //cn = common name

		searchRequest := ldap.NewSearchRequest(
			baseDN, searchScope, aliases, 0, 0, false,
			searchFilter, retrieveAttributes, nil,
		)

		searchResult, err := l.Search(searchRequest)
		if err != nil {
			log.Fatal(err)
		}

		// Encode user details into a JWT and turn it into a string
		jwtKey := []byte("secret_text")
		userFound := searchResult.Entries[0]
		expirationTime := time.Now().Add(time.Hour * 24)
		claims := &Claims{
			hashedZID: hashedZID,
			firstName: userFound.GetAttributeValue("firstName"),
			StandardClaims: jwt.StandardClaims{
				ExpiresAt: expirationTime.Unix(),
			},
		}
		tokenJWT := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tokenString, _ := tokenJWT.SignedString(jwtKey)

		// Insert a new user into the collection if the token has expired or has never logged in before
		user := User{
			userID:    string(hashedZID[:]),
			userToken: tokenString,
			role:      "user", // Change this???
		}

		var isValidUser *User
		userFilter := bson.D{{"userID", string(hashedZID[:])}}
		err = collection.FindOne(context.TODO(), userFilter).Decode(&isValidUser)

		if isValidUser == nil { // Never logged in before
			_, err = collection.InsertOne(context.TODO(), user)
			if err != nil {
				log.Fatal(err)
			}
		} else { // Logged in before - check validity of token
			claims = &Claims{}
			decodedToken, _ := jwt.ParseWithClaims(isValidUser.userToken, claims, func(token *jwt.Token) (interface{}, error) {
				return jwtKey, nil
			})
			decodedTokenString, _ := decodedToken.SignedString(jwtKey)

			if !decodedToken.Valid { // Logged in before but token is invalid - replace with new token
				filter := bson.D{{"userID", string(hashedZID[:])}}
				update := bson.D{
					{"$set", bson.D{
						{"userToken", decodedTokenString},
					}},
				}
				_, err = collection.UpdateOne(context.TODO(), filter, update)
				if err != nil {
					log.Fatal(err)
				}
			}
		}

		return c.JSON(http.StatusOK, H{
			"token": tokenString,
		})
	}
}

func getPost(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		var result *Post
		id, _ := strconv.Atoi(c.QueryParam("id"))
		category := c.QueryParam("category")

		// Search for post by id and category
		filter := bson.D{{"postID", id}, {"category", category}}
		err := collection.FindOne(context.TODO(), filter).Decode(&result)
		if err != nil {
			log.Fatal(err)
		}
		return c.JSON(http.StatusOK, H{
			"post": result,
		})
	}
}

func getAllPosts(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		count, _ := strconv.Atoi(c.QueryParam("id"))
		cat := c.QueryParam("category")

		findOptions := options.Find()
		if count != 10 {
			findOptions.SetLimit(int64(count))
		} else {
			findOptions.SetLimit(10)
		}

		var posts []*Post
		var cur *mongo.Cursor
		var err error

		if cat == "" { // No specified category
			cur, err = collection.Find(context.TODO(), bson.D{{}}, findOptions)
		} else {
			filter := bson.D{{"post_category", cat}}
			cur, err = collection.Find(context.TODO(), filter, findOptions)
		}

		if err != nil {
			log.Fatal(err)
		}

		// Iterate through all results
		for cur.Next(context.TODO()) {
			var elem Post
			err := cur.Decode(&elem)
			if err != nil {
				log.Fatal(err)
			}

			posts = append(posts, &elem)
		}

		return c.JSON(http.StatusOK, H{
			"posts": posts,
		})
	}
}

func newPost(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		id, _ := strconv.Atoi(c.FormValue("id"))
		category, _ := strconv.Atoi(c.FormValue("category"))
		showinMenu, _ := strconv.ParseBool(c.FormValue("showInMenu"))

		post := Post{
			postID:           id,
			postTitle:        c.FormValue("title"),
			postSubtitle:     c.FormValue("subtitle"),
			postType:         c.FormValue("type"),
			postCategory:     category,
			createdOn:        time.Now(),
			lastEditedOn:     time.Now(),
			postContent:      c.FormValue("content"),
			postLinkGithub:   c.FormValue("linkGithub"),
			postLinkFacebook: c.FormValue("linkFacebook"),
			showInMenu:       showinMenu,
		}

		_, err := collection.InsertOne(context.TODO(), post)
		if err != nil {
			log.Fatal(err)
		}

		return c.JSON(http.StatusOK, H{})
	}
}

func updatePost(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		postID, _ := strconv.Atoi(c.FormValue("id"))
		postTitle := c.FormValue("title")
		postSubtitle := c.FormValue("subtitle")
		postType := c.FormValue("type")
		postCategory := c.FormValue("category")
		postContent := c.FormValue("content")
		postLinkGithub := c.FormValue("linkGithub")
		postLinkFacebook := c.FormValue("linkFacebook")
		showinMenu, _ := strconv.ParseBool(c.FormValue("showInMenu"))

		filter := bson.D{{"postID", postID}}
		update := bson.D{
			{"$set", bson.D{
				{"postTitle", postTitle},
				{"postSubtitle", postSubtitle},
				{"postType", postType},
				{"postCategory", postCategory},
				{"lastEditedOn", time.Now()},
				{"postContent", postContent},
				{"postLinkGithub", postLinkGithub},
				{"postLinkFacebook", postLinkFacebook},
				{"showinMenu", showinMenu},
			}},
		}

		// Find a post by id and update it
		_, err := collection.UpdateOne(context.TODO(), filter, update)
		if err != nil {
			log.Fatal(err)
		}

		return c.JSON(http.StatusOK, H{})
	}
}

func deletePost(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		id, _ := strconv.Atoi(c.FormValue("id"))
		filter := bson.D{{"postID", id}}

		// Find a post by id and delete it
		_, err := collection.DeleteOne(context.TODO(), filter)
		if err != nil {
			log.Fatal(err)
		}

		return c.JSON(http.StatusOK, H{})
	}
}

func getCat(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		id, _ := strconv.Atoi(c.QueryParam("id"))
		var result *Category
		filter := bson.D{{"categoryID", id}}

		// Find a category
		err := collection.FindOne(context.TODO(), filter).Decode(&result)
		if err != nil {
			log.Fatal(err)
		}

		return c.JSON(http.StatusOK, H{
			"category": result,
		})
	}
}

func newCat(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		catID, _ := strconv.Atoi(c.FormValue("id"))
		index, _ := strconv.Atoi(c.FormValue("index"))

		category := Category{
			categoryID:   catID,
			categoryName: c.FormValue("name"),
			index:        index,
		}

		_, err := collection.InsertOne(context.TODO(), category)
		if err != nil {
			log.Fatal(err)
		}
		return c.JSON(http.StatusOK, H{})
	}
}

func patchCat(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		categoryID, _ := strconv.Atoi(c.FormValue("id"))
		categoryName := c.FormValue("name")
		index, _ := strconv.Atoi(c.FormValue("index"))
		filter := bson.D{{"categoryID", categoryID}}
		update := bson.D{
			{"$set", bson.D{
				{"categoryName", categoryName},
				{"index", index},
			}},
		}

		// Find a category by id and update it
		_, err := collection.UpdateOne(context.TODO(), filter, update)
		if err != nil {
			log.Fatal(err)
		}

		return c.JSON(http.StatusOK, H{})
	}
}

func deleteCat(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		id, _ := strconv.Atoi(c.FormValue("id"))
		filter := bson.D{{"categoryID", id}}

		// Find a category by id and delete it
		_, err := collection.DeleteOne(context.TODO(), filter)
		if err != nil {
			log.Fatal(err)
		}

		return c.JSON(http.StatusOK, H{})
	}
}

func newSponsor(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		expiryStr := c.FormValue("expiry")
		expiryTime, _ := time.Parse(time.RFC3339, expiryStr)
		id := uuid.New()

		sponsor := Sponsor{
			sponsorID:   id,
			sponsorName: c.FormValue("name"),
			sponsorLogo: c.FormValue("logo"),
			sponsorTier: c.FormValue("tier"),
			expiry:      expiryTime.Unix(),
		}

		_, err := collection.InsertOne(context.TODO(), sponsor)
		if err != nil {
			log.Fatal(err)
		}
		return c.JSON(http.StatusOK, H{})
	}
}

func deleteSponsor(collection *mongo.Collection) echo.HandlerFunc {
	return func(c echo.Context) error {
		id := c.FormValue("id")
		parsedID := uuid.Must(uuid.Parse(id))

		// Find a sponsor by ID and delete it
		filter := bson.D{{"sponsorID", parsedID}}
		_, err := collection.DeleteOne(context.TODO(), filter)
		if err != nil {
			log.Fatal(err)
		}

		return c.JSON(http.StatusOK, H{})
	}
}
