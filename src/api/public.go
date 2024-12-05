/**
 * public.go - / rest api implementation
 *
 * @author Mike Schroeder <m.schroeder223@gmail.com>
 */
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

/**
 * Attaches / handlers
 */
func attachPublic(app *gin.RouterGroup) {
	/**
	 * Simple 200 and OK response
	 */
	app.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "OK")
	})
}
